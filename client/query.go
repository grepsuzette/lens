package client

import (
	"context"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	querytypes "github.com/cosmos/cosmos-sdk/types/query"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bankTypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distTypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	transfertypes "github.com/cosmos/ibc-go/v2/modules/apps/transfer/types"
)

// queryBalanceWithAddress returns the amount of coins in the relayer account with address as input
// TODO add pagination support
func (cc *ChainClient) queryBalanceWithAddress(address string) (sdk.Coins, error) {
	p := &bankTypes.QueryAllBalancesRequest{Address: address, Pagination: DefaultPageRequest()}
	queryClient := bankTypes.NewQueryClient(cc)

	res, err := queryClient.AllBalances(context.Background(), p)
	if err != nil {
		return nil, err
	}

	return res.Balances, nil
}

func (cc *ChainClient) queryLatestHeight() (int64, error) {
	stat, err := cc.RPCClient.Status(context.Background())
	if err != nil {
		return -1, err
	} else if stat.SyncInfo.CatchingUp {
		return -1, fmt.Errorf("node at %s running chain %s not caught up", cc.Config.RPCAddr, cc.Config.ChainID)
	}
	return stat.SyncInfo.LatestBlockHeight, nil
}

// queryDenomTraces returns all the denom traces from a given chain
// TODO add pagination support
func (cc *ChainClient) queryDenomTraces(offset, limit uint64, height int64) ([]transfertypes.DenomTrace, error) {
	transfers, err := transfertypes.NewQueryClient(cc).DenomTraces(context.Background(),
		&transfertypes.QueryDenomTracesRequest{
			Pagination: DefaultPageRequest(),
		})
	if err != nil {
		return nil, err
	}
	return transfers.DenomTraces, nil
}

func (cc *ChainClient) QueryAccount(address sdk.AccAddress) (authtypes.AccountI, error) {
	addr, err := cc.EncodeBech32AccAddr(address)
	if err != nil {
		return nil, err
	}
	res, err := authtypes.NewQueryClient(cc).Account(context.Background(), &authtypes.QueryAccountRequest{Address: addr})
	if err != nil {
		return nil, err
	}
	var acc authtypes.AccountI
	if err := cc.Codec.InterfaceRegistry.UnpackAny(res.Account, &acc); err != nil {
		return nil, err
	}
	return acc, nil
}

// QueryBalanceWithDenomTraces is a helper function for query balance
func (cc *ChainClient) QueryBalanceWithDenomTraces(ctx context.Context, address sdk.AccAddress, pageReq *query.PageRequest) (sdk.Coins, error) {
	coins, err := cc.queryBalanceWithAddress(cc.MustEncodeAccAddr(address))
	if err != nil {
		return nil, err
	}

	h, err := cc.queryLatestHeight()
	if err != nil {
		return nil, err
	}

	// TODO: figure out how to handle this
	// we don't want to expose user to this
	// so maybe we need a QueryAllDenomTraces function
	// that will paginate the responses automatically
	// TODO fix pagination here later
	dts, err := cc.queryDenomTraces(0, 1000, h)
	if err != nil {
		return nil, err
	}

	if len(dts) == 0 {
		return coins, nil
	}

	var out sdk.Coins
	for _, c := range coins {
		if c.Amount.Equal(sdk.NewInt(0)) {
			continue
		}

		for i, d := range dts {
			if c.Denom == d.IBCDenom() {
				out = append(out, sdk.Coin{Denom: d.GetFullDenomPath(), Amount: c.Amount})
				break
			}

			if i == len(dts)-1 {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (cc *ChainClient) QueryDelegatorValidators(ctx context.Context, address sdk.AccAddress) ([]string, error) {
	res, err := distTypes.NewQueryClient(cc).DelegatorValidators(ctx, &distTypes.QueryDelegatorValidatorsRequest{
		DelegatorAddress: cc.MustEncodeAccAddr(address),
	})
	if err != nil {
		return nil, err
	}
	return res.Validators, nil
}

func (cc *ChainClient) QueryDistributionCommission(ctx context.Context, address sdk.ValAddress) (sdk.DecCoins, error) {
	valAddr, err := cc.EncodeBech32ValAddr(address)
	if err != nil {
		return nil, err
	}
	request := distTypes.QueryValidatorCommissionRequest{
		ValidatorAddress: valAddr,
	}
	res, err := distTypes.NewQueryClient(cc).ValidatorCommission(ctx, &request)
	if err != nil {
		return nil, err
	}
	return res.Commission.Commission, nil
}

func (cc *ChainClient) QueryDistributionCommunityPool(ctx context.Context) (sdk.DecCoins, error) {
	res, err := distTypes.NewQueryClient(cc).CommunityPool(ctx, &distTypes.QueryCommunityPoolRequest{})
	if err != nil {
		return nil, err
	}
	return res.Pool, err
}

func (cc *ChainClient) QueryDistributionParams(ctx context.Context) (*distTypes.Params, error) {
	res, err := distTypes.NewQueryClient(cc).Params(ctx, &distTypes.QueryParamsRequest{})
	if err != nil {
		return nil, err
	}
	return &res.Params, nil
}

func (cc *ChainClient) QueryDistributionRewards(ctx context.Context, delegatorAddress sdk.AccAddress, validatorAddress sdk.ValAddress) (sdk.DecCoins, error) {
	delAddr, err := cc.EncodeBech32AccAddr(delegatorAddress)
	if err != nil {
		return nil, err
	}
	valAddr, err := cc.EncodeBech32ValAddr(validatorAddress)
	if err != nil {
		return nil, err
	}
	request := distTypes.QueryDelegationRewardsRequest{
		DelegatorAddress: delAddr,
		ValidatorAddress: valAddr,
	}
	res, err := distTypes.NewQueryClient(cc).DelegationRewards(ctx, &request)
	if err != nil {
		return nil, err
	}
	return res.Rewards, nil
}

// QueryDistributionSlashes returns all slashes of a validator, optionally pass the start and end height
func (cc *ChainClient) QueryDistributionSlashes(ctx context.Context, validatorAddress sdk.ValAddress, startHeight, endHeight uint64, pageReq *querytypes.PageRequest) (*distTypes.QueryValidatorSlashesResponse, error) {
	valAddr, err := cc.EncodeBech32ValAddr(validatorAddress)
	if err != nil {
		return nil, err
	}
	request := distTypes.QueryValidatorSlashesRequest{
		ValidatorAddress: valAddr,
		StartingHeight:   startHeight,
		EndingHeight:     endHeight,
		Pagination:       pageReq,
	}
	return distTypes.NewQueryClient(cc).ValidatorSlashes(ctx, &request)
}

// QueryDistributionValidatorRewards returns all the validator distribution rewards from a given height
func (cc *ChainClient) QueryDistributionValidatorRewards(ctx context.Context, validatorAddress sdk.ValAddress) (sdk.DecCoins, error) {
	valAddr, err := cc.EncodeBech32ValAddr(validatorAddress)
	if err != nil {
		return nil, err
	}
	request := distTypes.QueryValidatorOutstandingRewardsRequest{
		ValidatorAddress: valAddr,
	}
	res, err := distTypes.NewQueryClient(cc).ValidatorOutstandingRewards(ctx, &request)
	if err != nil {
		return nil, err
	}
	return res.Rewards.Rewards, nil
}

// QueryTotalSupply returns the total supply of coins on a chain
func (cc *ChainClient) QueryTotalSupply(ctx context.Context, pageReq *query.PageRequest) (*bankTypes.QueryTotalSupplyResponse, error) {
	return bankTypes.NewQueryClient(cc).TotalSupply(ctx, &bankTypes.QueryTotalSupplyRequest{Pagination: pageReq})
}

func (cc *ChainClient) QueryDenomsMetadata(ctx context.Context, pageReq *query.PageRequest) (*bankTypes.QueryDenomsMetadataResponse, error) {
	return bankTypes.NewQueryClient(cc).DenomsMetadata(ctx, &bankTypes.QueryDenomsMetadataRequest{Pagination: pageReq})
}

func DefaultPageRequest() *querytypes.PageRequest {
	return &querytypes.PageRequest{
		Key:        []byte(""),
		Offset:     0,
		Limit:      1000,
		CountTotal: true,
	}
}
