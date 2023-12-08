package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"

	"cosmossdk.io/math"
	bbntypes "github.com/babylonchain/babylon/types"
	"google.golang.org/grpc"

	"github.com/babylonchain/btc-validator/types"
	"github.com/babylonchain/btc-validator/validator/proto"
	"github.com/babylonchain/btc-validator/version"
)

// rpcServer is the main RPC server for the BTC-Validator daemon that handles
// gRPC incoming requests.
type rpcServer struct {
	started  int32
	shutdown int32

	proto.UnimplementedBtcValidatorsServer

	app *ValidatorApp

	quit chan struct{}
	wg   sync.WaitGroup
}

// newRPCServer creates a new RPC sever from the set of input dependencies.
func newRPCServer(
	v *ValidatorApp,
) *rpcServer {

	return &rpcServer{
		quit: make(chan struct{}),
		app:  v,
	}
}

// Start signals that the RPC server starts accepting requests.
func (r *rpcServer) Start() error {
	if atomic.AddInt32(&r.started, 1) != 1 {
		return nil
	}

	return nil
}

// Stop signals that the RPC server should attempt a graceful shutdown and
// cancel any outstanding requests.
func (r *rpcServer) Stop() error {
	if atomic.AddInt32(&r.shutdown, 1) != 1 {
		return nil
	}

	close(r.quit)

	r.wg.Wait()

	return nil
}

// RegisterWithGrpcServer registers the rpcServer with the passed root gRPC
// server.
func (r *rpcServer) RegisterWithGrpcServer(grpcServer *grpc.Server) error {
	// Register the main RPC server.
	proto.RegisterBtcValidatorsServer(grpcServer, r)
	return nil
}

// GetInfo returns general information relating to the active daemon
func (r *rpcServer) GetInfo(context.Context, *proto.GetInfoRequest) (*proto.GetInfoResponse, error) {

	return &proto.GetInfoResponse{
		Version: version.Version(),
	}, nil
}

// CreateValidator generates a validator object and saves it in the database
func (r *rpcServer) CreateValidator(ctx context.Context, req *proto.CreateValidatorRequest) (
	*proto.CreateValidatorResponse, error) {

	commissionRate, err := math.LegacyNewDecFromStr(req.Commission)
	if err != nil {
		return nil, err
	}

	result, err := r.app.CreateValidator(
		req.KeyName,
		req.ChainId,
		req.Passphrase,
		req.HdPath,
		req.Description,
		&commissionRate,
	)

	if err != nil {
		return nil, err
	}

	return &proto.CreateValidatorResponse{
		BtcPk: result.ValPk.MarshalHex(),
	}, nil

}

// RegisterValidator sends a transactions to Babylon to register a BTC validator
func (r *rpcServer) RegisterValidator(ctx context.Context, req *proto.RegisterValidatorRequest) (
	*proto.RegisterValidatorResponse, error) {

	txRes, err := r.app.RegisterValidator(req.BtcPk)
	if err != nil {
		return nil, fmt.Errorf("failed to register the validator to Babylon: %w", err)
	}

	// the validator instance should be started right after registration
	if err := r.app.StartHandlingValidator(txRes.btcPubKey, req.Passphrase); err != nil {
		return nil, fmt.Errorf("failed to start the registered validator %s: %w", hex.EncodeToString(txRes.bbnPubKey.Key), err)
	}

	return &proto.RegisterValidatorResponse{TxHash: txRes.TxHash}, nil
}

// AddFinalitySignature adds a manually constructed finality signature to Babylon
// NOTE: this is only used for presentation/testing purposes
func (r *rpcServer) AddFinalitySignature(ctx context.Context, req *proto.AddFinalitySignatureRequest) (
	*proto.AddFinalitySignatureResponse, error) {

	valPk, err := bbntypes.NewBIP340PubKeyFromHex(req.BtcPk)
	if err != nil {
		return nil, err
	}

	v, err := r.app.GetValidatorInstance(valPk)
	if err != nil {
		return nil, err
	}

	b := &types.BlockInfo{
		Height: req.Height,
		Hash:   req.AppHash,
	}

	txRes, privKey, err := v.TestSubmitFinalitySignatureAndExtractPrivKey(b)
	if err != nil {
		return nil, err
	}

	res := &proto.AddFinalitySignatureResponse{TxHash: txRes.TxHash}

	// if privKey is not empty, then this BTC validator
	// has voted for a fork and will be slashed
	if privKey != nil {
		localPrivKey, err := r.app.getValPrivKey(valPk.MustMarshal())
		res.ExtractedSkHex = privKey.Key.String()
		if err != nil {
			return nil, err
		}
		localSkHex := localPrivKey.Key.String()
		localSkNegateHex := localPrivKey.Key.Negate().String()
		if res.ExtractedSkHex == localSkHex {
			res.LocalSkHex = localSkHex
		} else if res.ExtractedSkHex == localSkNegateHex {
			res.LocalSkHex = localSkNegateHex
		} else {
			return nil, fmt.Errorf("the validator's BTC private key is extracted but does not match the local key,"+
				"extrated: %s, local: %s, local-negated: %s",
				res.ExtractedSkHex, localSkHex, localSkNegateHex)
		}
	}

	return res, nil
}

// QueryValidator queries the information of the validator
func (r *rpcServer) QueryValidator(ctx context.Context, req *proto.QueryValidatorRequest) (
	*proto.QueryValidatorResponse, error) {

	valPk, err := bbntypes.NewBIP340PubKeyFromHex(req.BtcPk)
	if err != nil {
		return nil, err
	}
	val, err := r.app.GetValidatorInstance(valPk)
	if err != nil {
		return nil, err
	}

	valInfo := proto.NewValidatorInfo(val.GetStoreValidator())

	return &proto.QueryValidatorResponse{Validator: valInfo}, nil
}

// QueryValidatorList queries the information of a list of validators
func (r *rpcServer) QueryValidatorList(ctx context.Context, req *proto.QueryValidatorListRequest) (
	*proto.QueryValidatorListResponse, error) {

	vals := r.app.ListValidatorInstances()

	valsInfo := make([]*proto.ValidatorInfo, len(vals))
	for i, v := range vals {
		valInfo := proto.NewValidatorInfo(v.GetStoreValidator())
		valsInfo[i] = valInfo
	}

	return &proto.QueryValidatorListResponse{Validators: valsInfo}, nil
}