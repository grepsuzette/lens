package client

import (
	"context"
	"fmt"
	"github.com/avast/retry-go"
	"strings"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/store/rootmulti"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	abci "github.com/tendermint/tendermint/abci/types"
	rpcclient "github.com/tendermint/tendermint/rpc/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (cc *ChainClient) TxFactory() tx.Factory {
	return tx.Factory{}.
		WithAccountRetriever(cc).
		WithChainID(cc.Config.ChainID).
		WithTxConfig(cc.Codec.TxConfig).
		WithGasAdjustment(cc.Config.GasAdjustment).
		WithGasPrices(cc.Config.GasPrices).
		WithKeybase(cc.Keybase).
		WithSignMode(cc.Config.SignMode())
}

func (ccc *ChainClientConfig) SignMode() signing.SignMode {
	signMode := signing.SignMode_SIGN_MODE_UNSPECIFIED
	switch ccc.SignModeStr {
	case "direct":
		signMode = signing.SignMode_SIGN_MODE_DIRECT
	case "amino-json":
		signMode = signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON
	}
	return signMode
}

func (cc *ChainClient) SendMsg(ctx context.Context, msg sdk.Msg) (*sdk.TxResponse, error) {
	return cc.SendMsgs(ctx, []sdk.Msg{msg})
}

// SendMsgs wraps the msgs in a StdTx, signs and sends it. An error is returned if there
// was an issue sending the transaction. A successfully sent, but failed transaction will
// not return an error. If a transaction is successfully sent, the result of the execution
// of that transaction will be logged. A boolean indicating if a transaction was successfully
// sent and executed successfully is returned.
func (cc *ChainClient) SendMsgs(ctx context.Context, msgs []sdk.Msg) (*sdk.TxResponse, error) {
	txf, err := cc.PrepareFactory(cc.TxFactory())
	if err != nil {
		return nil, err
	}

	// TODO: Make this work with new CalculateGas method
	// TODO: This is related to GRPC client stuff?
	// https://github.com/cosmos/cosmos-sdk/blob/5725659684fc93790a63981c653feee33ecf3225/client/tx/tx.go#L297
	_, adjusted, err := cc.CalculateGas(txf, msgs...)
	if err != nil {
		return nil, err
	}

	// Set the gas amount on the transaction factory
	txf = txf.WithGas(adjusted)

	// Build the transaction builder
	txb, err := tx.BuildUnsignedTx(txf, msgs...)
	if err != nil {
		return nil, err
	}

	// Attach the signature to the transaction
	// c.LogFailedTx(nil, err, msgs)
	// Force encoding in the chain specific address
	for _, msg := range msgs {
		cc.Codec.Marshaler.MustMarshalJSON(msg)
	}

	done := cc.SetSDKContext()
	if err = tx.Sign(txf, cc.Config.Key, txb, false); err != nil {
		return nil, err
	}
	done()

	// Generate the transaction bytes
	txBytes, err := cc.Codec.TxConfig.TxEncoder()(txb.GetTx())
	if err != nil {
		return nil, err
	}

	// Broadcast those bytes
	res, err := cc.BroadcastTx(ctx, txBytes)
	if err != nil {
		return nil, err
	}

	// transaction was executed, log the success or failure using the tx response code
	// NOTE: error is nil, logic should use the returned error to determine if the
	// transaction was successfully executed.
	if res.Code != 0 {
		return res, fmt.Errorf("transaction failed with code: %d", res.Code)
	}

	return res, nil
}

func (cc *ChainClient) PrepareFactory(txf tx.Factory) (tx.Factory, error) {
	var (
		err      error
		from     sdk.AccAddress
		num, seq uint64
	)

	// Get key address and retry if fail
	if err = retry.Do(func() error {
		from, err = cc.GetKeyAddress()
		if err != nil {
			return err
		}
		return err
	}, RtyAtt, RtyDel, RtyErr); err != nil {
		return tx.Factory{}, err
	}

	cliCtx := client.Context{}.WithClient(cc.RPCClient).
		WithInterfaceRegistry(cc.Codec.InterfaceRegistry).
		WithChainID(cc.Config.ChainID).
		WithCodec(cc.Codec.Marshaler)

	// Set the account number and sequence on the transaction factory and retry if fail
	if err = retry.Do(func() error {
		if err = txf.AccountRetriever().EnsureExists(cliCtx, from); err != nil {
			return err
		}
		return err
	}, RtyAtt, RtyDel, RtyErr); err != nil {
		return txf, err
	}

	// TODO: why this code? this may potentially require another query when we don't want one
	initNum, initSeq := txf.AccountNumber(), txf.Sequence()
	if initNum == 0 || initSeq == 0 {
		if err = retry.Do(func() error {
			num, seq, err = txf.AccountRetriever().GetAccountNumberSequence(cliCtx, from)
			if err != nil {
				return err
			}
			return err
		}, RtyAtt, RtyDel, RtyErr); err != nil {
			return txf, err
		}

		if initNum == 0 {
			txf = txf.WithAccountNumber(num)
		}

		if initSeq == 0 {
			txf = txf.WithSequence(seq)
		}
	}

	return txf, nil
}

func (cc *ChainClient) CalculateGas(txf tx.Factory, msgs ...sdk.Msg) (txtypes.SimulateResponse, uint64, error) {
	var (
		txBytes []byte
		err     error
		res     abci.ResponseQuery
		simRes  txtypes.SimulateResponse
	)

	if err = retry.Do(func() error {
		txBytes, err = BuildSimTx(txf, msgs...)
		if err != nil {
			return err
		}
		return err
	}, RtyAtt, RtyDel, RtyErr); err != nil {
		return txtypes.SimulateResponse{}, 0, err
	}

	simQuery := abci.RequestQuery{
		Path: "/cosmos.tx.v1beta1.Service/Simulate",
		Data: txBytes,
	}

	if err = retry.Do(func() error {
		res, err = cc.QueryABCI(simQuery)
		if err != nil {
			return err
		}
		return err
	}, RtyAtt, RtyDel, RtyErr); err != nil {
		return txtypes.SimulateResponse{}, 0, err
	}

	if err = retry.Do(func() error {
		if err = simRes.Unmarshal(res.Value); err != nil {
			return err
		}
		return err
	}, RtyAtt, RtyDel, RtyErr); err != nil {
		return txtypes.SimulateResponse{}, 0, err
	}

	return simRes, uint64(txf.GasAdjustment() * float64(simRes.GasInfo.GasUsed)), nil
}

func (cc *ChainClient) QueryABCI(req abci.RequestQuery) (abci.ResponseQuery, error) {
	opts := rpcclient.ABCIQueryOptions{
		Height: req.Height,
		Prove:  req.Prove,
	}
	result, err := cc.RPCClient.ABCIQueryWithOptions(context.Background(), req.Path, req.Data, opts)
	if err != nil {
		return abci.ResponseQuery{}, err
	}

	if !result.Response.IsOK() {
		return abci.ResponseQuery{}, sdkErrorToGRPCError(result.Response)
	}

	// data from trusted node or subspace query doesn't need verification
	if !opts.Prove || !isQueryStoreWithProof(req.Path) {
		return result.Response, nil
	}

	return result.Response, nil
}

func sdkErrorToGRPCError(resp abci.ResponseQuery) error {
	switch resp.Code {
	case sdkerrors.ErrInvalidRequest.ABCICode():
		return status.Error(codes.InvalidArgument, resp.Log)
	case sdkerrors.ErrUnauthorized.ABCICode():
		return status.Error(codes.Unauthenticated, resp.Log)
	case sdkerrors.ErrKeyNotFound.ABCICode():
		return status.Error(codes.NotFound, resp.Log)
	default:
		return status.Error(codes.Unknown, resp.Log)
	}
}

// isQueryStoreWithProof expects a format like /<queryType>/<storeName>/<subpath>
// queryType must be "store" and subpath must be "key" to require a proof.
func isQueryStoreWithProof(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}

	paths := strings.SplitN(path[1:], "/", 3)

	switch {
	case len(paths) != 3:
		return false
	case paths[0] != "store":
		return false
	case rootmulti.RequireProof("/" + paths[2]):
		return true
	}

	return false
}

// protoTxProvider is a type which can provide a proto transaction. It is a
// workaround to get access to the wrapper TxBuilder's method GetProtoTx().
type protoTxProvider interface {
	GetProtoTx() *txtypes.Tx
}

// BuildSimTx creates an unsigned tx with an empty single signature and returns
// the encoded transaction or an error if the unsigned transaction cannot be built.
func BuildSimTx(txf tx.Factory, msgs ...sdk.Msg) ([]byte, error) {
	txb, err := tx.BuildUnsignedTx(txf, msgs...)
	if err != nil {
		return nil, err
	}

	// Create an empty signature literal as the ante handler will populate with a
	// sentinel pubkey.
	sig := signing.SignatureV2{
		PubKey: &secp256k1.PubKey{},
		Data: &signing.SingleSignatureData{
			SignMode: txf.SignMode(),
		},
		Sequence: txf.Sequence(),
	}
	if err := txb.SetSignatures(sig); err != nil {
		return nil, err
	}

	protoProvider, ok := txb.(protoTxProvider)
	if !ok {
		return nil, fmt.Errorf("cannot simulate amino tx")
	}

	simReq := txtypes.SimulateRequest{Tx: protoProvider.GetProtoTx()}
	return simReq.Marshal()
}
