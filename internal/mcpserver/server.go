package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/BeLazy167/hl-mcp/internal/audit"
	"github.com/BeLazy167/hl-mcp/internal/hyperliquid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	version                = "1.4.0"
	maximumFenceExpiration = 5 * time.Minute
)

type hyperliquidAPI interface {
	AccountIdentity(context.Context) (hyperliquid.AccountIdentity, error)
	Balance(context.Context) (hyperliquid.Balance, error)
	Positions(context.Context) ([]hyperliquid.Position, error)
	Ticker(context.Context, string) (hyperliquid.Ticker, error)
	SearchMarkets(string, int) ([]hyperliquid.MarketSummary, error)
	OpenOrders(context.Context, *string) ([]hyperliquid.OpenOrder, error)
	OrderBook(context.Context, hyperliquid.OrderBookParams) (hyperliquid.OrderBook, error)
	Candles(context.Context, hyperliquid.CandlesParams) ([]hyperliquid.Candle, error)
	UserFills(context.Context, hyperliquid.UserFillsParams) ([]json.RawMessage, error)
	OrderHistory(context.Context, int) ([]json.RawMessage, error)
	OrderStatus(context.Context, string) (json.RawMessage, error)
	FundingHistory(context.Context, hyperliquid.FundingHistoryParams) ([]json.RawMessage, error)
	UserFunding(context.Context, hyperliquid.UserFundingParams) ([]json.RawMessage, error)
	PredictedFunding(context.Context, *string) ([]json.RawMessage, error)
	Portfolio(context.Context) (json.RawMessage, error)
	Fees(context.Context) (json.RawMessage, error)
	RateLimit(context.Context) (json.RawMessage, error)
	SpotBalances(context.Context) ([]hyperliquid.SpotBalance, error)
	ActiveAssetData(context.Context, string) (hyperliquid.ActiveAssetData, error)
	ReserveNonce() uint64
	SetLeverageWithOptions(context.Context, string, int, hyperliquid.MutationOptions) (hyperliquid.MutationResult, error)
	PlaceOrderWithOptions(context.Context, hyperliquid.PlaceOrderParams, hyperliquid.MutationOptions) (hyperliquid.MutationResult, error)
	CancelOrderWithOptions(context.Context, string, string, hyperliquid.MutationOptions) (hyperliquid.MutationResult, error)
	CancelAllWithOptions(context.Context, string, hyperliquid.MutationOptions) (hyperliquid.MutationResult, error)
	RequestTimeout() time.Duration
}

type Server struct {
	hl               hyperliquidAPI
	audit            *audit.Store
	mcp              *mcp.Server
	fencedMutationMu sync.Mutex
}

type emptyInput struct{}
type tickerInput struct {
	Symbol string `json:"symbol"`
}
type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}
type openOrdersInput struct {
	Symbol *string `json:"symbol,omitempty"`
}
type orderBookInput struct {
	Symbol   string `json:"symbol"`
	NSigFigs *int   `json:"nSigFigs,omitempty"`
	Mantissa *int   `json:"mantissa,omitempty"`
}
type candlesInput struct {
	Symbol    string `json:"symbol"`
	Interval  string `json:"interval"`
	StartTime int64  `json:"startTime"`
	EndTime   int64  `json:"endTime"`
	Limit     int    `json:"limit,omitempty"`
}
type userFillsInput struct {
	StartTime       *int64 `json:"startTime,omitempty"`
	EndTime         *int64 `json:"endTime,omitempty"`
	AggregateByTime bool   `json:"aggregateByTime,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}
type orderHistoryInput struct {
	Limit int `json:"limit,omitempty"`
}
type orderStatusInput struct {
	ID string `json:"id"`
}
type fundingHistoryInput struct {
	Symbol    string `json:"symbol"`
	StartTime int64  `json:"startTime"`
	EndTime   *int64 `json:"endTime,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type userFundingInput struct {
	StartTime int64  `json:"startTime"`
	EndTime   *int64 `json:"endTime,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type predictedFundingInput struct {
	Symbol *string `json:"symbol,omitempty"`
}
type activeAssetDataInput struct {
	Symbol string `json:"symbol"`
}
type mutationContractOutput struct {
	ContractVersion int `json:"contractVersion"`
	Fence           struct {
		ReservationTool                string `json:"reservationTool"`
		GenerationField                string `json:"generationField"`
		ExpiryField                    string `json:"expiryField"`
		MaxExpirationMs                int64  `json:"maxExpirationMs"`
		SignedExpiresAfter             bool   `json:"signedExpiresAfter"`
		WriterOwnedGenerations         bool   `json:"writerOwnedGenerations"`
		ExactCurrentGenerationRequired bool   `json:"exactCurrentGenerationRequired"`
		CrossProcessSendLock           bool   `json:"crossProcessSendLock"`
	} `json:"fence"`
	MutationTools []string `json:"mutationTools"`
}

func newMutationContract() mutationContractOutput {
	var contract mutationContractOutput
	contract.ContractVersion = 2
	contract.Fence.ReservationTool = "hl_reserve_fence"
	contract.Fence.GenerationField = "fenceGeneration"
	contract.Fence.ExpiryField = "fenceExpiresAtMs"
	contract.Fence.MaxExpirationMs = int64(maximumFenceExpiration / time.Millisecond)
	contract.Fence.SignedExpiresAfter = true
	contract.Fence.WriterOwnedGenerations = true
	contract.Fence.ExactCurrentGenerationRequired = true
	contract.Fence.CrossProcessSendLock = true
	contract.MutationTools = []string{"hl_set_leverage", "hl_place_order"}
	return contract
}

type reserveFenceInput struct {
	FenceExpiresAtMs int64  `json:"fenceExpiresAtMs"`
	OwnerGeneration  *int64 `json:"ownerGeneration,omitempty"`
}
type reserveFenceOutput struct {
	FenceGeneration  int64  `json:"fenceGeneration"`
	FenceExpiresAtMs int64  `json:"fenceExpiresAtMs"`
	OwnerGeneration  *int64 `json:"ownerGeneration,omitempty"`
}
type leverageInput struct {
	Symbol           string `json:"symbol"`
	Leverage         int    `json:"leverage"`
	FenceGeneration  int64  `json:"fenceGeneration"`
	FenceExpiresAtMs int64  `json:"fenceExpiresAtMs"`
}
type placeOrderInput struct {
	Symbol                  string   `json:"symbol"`
	Side                    string   `json:"side"`
	Amount                  float64  `json:"amount"`
	Price                   *float64 `json:"price,omitempty"`
	StopLoss                *float64 `json:"stopLoss,omitempty"`
	StopLossLimitPrice      *float64 `json:"stopLossLimitPrice,omitempty"`
	TakeProfit              *float64 `json:"takeProfit,omitempty"`
	TakeProfitLimitPrice    *float64 `json:"takeProfitLimitPrice,omitempty"`
	ReduceOnly              bool     `json:"reduceOnly,omitempty"`
	ParentClientOrderID     *string  `json:"parentClientOrderId,omitempty"`
	TakeProfitClientOrderID *string  `json:"takeProfitClientOrderId,omitempty"`
	StopLossClientOrderID   *string  `json:"stopLossClientOrderId,omitempty"`
	FenceGeneration         int64    `json:"fenceGeneration"`
	FenceExpiresAtMs        int64    `json:"fenceExpiresAtMs"`
}
type cancelOrderInput struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
}
type cancelAllInput struct {
	Symbol string `json:"symbol"`
}
type getTradesInput struct {
	Limit           int    `json:"limit,omitempty"`
	BeforeID        int64  `json:"beforeId,omitempty"`
	Symbol          string `json:"symbol,omitempty"`
	Action          string `json:"action,omitempty"`
	Status          string `json:"status,omitempty"`
	IncludeResponse bool   `json:"includeResponse,omitempty"`
}

func New(hl hyperliquidAPI, auditStore *audit.Store) *Server {
	server := &Server{hl: hl, audit: auditStore}
	server.mcp = mcp.NewServer(&mcp.Implementation{Name: "hl-mcp", Version: version}, nil)
	server.registerTools()
	return server
}

func (s *Server) MCP() *mcp.Server { return s.mcp }

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, readTool(
		"hl_account_identity", "Account identity",
		"Current signer-to-funded-account authorization and network from userRole. Returns public identity only.",
		objectSchema(nil),
	), s.accountIdentity)
	mcp.AddTool(s.mcp, readTool(
		"hl_balance", "Hyperliquid balance",
		"Account USDC balance (free/used/total). On a unified account this is the spot collateral backing perps.",
		objectSchema(nil),
	), s.balance)
	mcp.AddTool(s.mcp, readTool(
		"hl_positions", "Open positions",
		"Open Hyperliquid positions with size, entry, liquidation price and unrealised PnL.",
		objectSchema(nil),
	), s.positions)
	mcp.AddTool(s.mcp, readTool(
		"hl_ticker", "Price quote",
		"Last/bid/ask for one market. Perp symbols look like BTC/USDC:USDC. Non-crypto instruments use an XYZ- prefix, e.g. XYZ-GOLD/USDC:USDC.",
		objectSchema(map[string]any{
			"symbol": stringSchema("e.g. BTC/USDC:USDC or XYZ-GOLD/USDC:USDC"),
		}, "symbol"),
	), s.ticker)
	mcp.AddTool(s.mcp, readTool(
		"hl_search_markets", "Search markets",
		"Find configured tradable symbols by substring. Non-crypto instruments such as GOLD, SILVER, BRENTOIL, and SP500 use the XYZ- prefix.",
		objectSchema(map[string]any{
			"query": stringSchema("substring, e.g. GOLD, SILVER, OIL, SP500, BTC"),
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25},
		}, "query"),
	), s.searchMarkets)
	mcp.AddTool(s.mcp, readTool(
		"hl_open_orders", "Open orders",
		"Resting orders, optionally filtered to one symbol. Unfiltered reads query every configured DEX.",
		objectSchema(map[string]any{"symbol": stringSchema("")}),
	), s.openOrders)
	mcp.AddTool(s.mcp, readTool(
		"hl_order_book", "Order book",
		"Read up to 20 bid and ask levels for one configured market.",
		objectSchema(map[string]any{
			"symbol":   stringSchema("configured perpetual symbol"),
			"nSigFigs": map[string]any{"type": "integer", "enum": []int{2, 3, 4, 5}},
			"mantissa": map[string]any{"type": "integer", "enum": []int{1, 2, 5}, "description": "requires nSigFigs=5"},
		}, "symbol"),
	), s.orderBook)
	mcp.AddTool(s.mcp, readTool(
		"hl_candles", "Candles",
		"Read one bounded page of OHLCV candles for a configured market. Hyperliquid retains the latest 5000 candles.",
		objectSchema(map[string]any{
			"symbol":    stringSchema("configured perpetual symbol"),
			"interval":  map[string]any{"type": "string", "enum": []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "8h", "12h", "1d", "3d", "1w", "1M"}},
			"startTime": unixMillisSchema("inclusive start time"),
			"endTime":   unixMillisSchema("inclusive end time"),
			"limit":     limitSchema(5000, 500),
		}, "symbol", "interval", "startTime", "endTime"),
	), s.candles)
	mcp.AddTool(s.mcp, readTool(
		"hl_user_fills", "User fills",
		"Read recent fills for the configured account. Supply startTime for a bounded time-range request.",
		objectSchema(map[string]any{
			"startTime":       unixMillisSchema("inclusive start time; selects userFillsByTime"),
			"endTime":         unixMillisSchema("inclusive end time; requires startTime"),
			"aggregateByTime": map[string]any{"type": "boolean", "default": false},
			"limit":           limitSchema(2000, 100),
		}),
	), s.userFills)
	mcp.AddTool(s.mcp, readTool(
		"hl_order_history", "Order history",
		"Read the configured account's most recent historical orders.",
		objectSchema(map[string]any{"limit": limitSchema(2000, 100)}),
	), s.orderHistory)
	mcp.AddTool(s.mcp, readTool(
		"hl_order_status", "Order status",
		"Read one order by exchange order ID or 16-byte client order ID.",
		objectSchema(map[string]any{
			"id": map[string]any{"type": "string", "pattern": `^(?:[0-9]+|0x[0-9a-fA-F]{32})$`},
		}, "id"),
	), s.orderStatus)
	mcp.AddTool(s.mcp, readTool(
		"hl_funding_history", "Funding history",
		"Read one bounded page of public funding rates for a configured market.",
		objectSchema(map[string]any{
			"symbol":    stringSchema("configured perpetual symbol"),
			"startTime": unixMillisSchema("inclusive start time"),
			"endTime":   unixMillisSchema("inclusive end time"),
			"limit":     limitSchema(500, 100),
		}, "symbol", "startTime"),
	), s.fundingHistory)
	mcp.AddTool(s.mcp, readTool(
		"hl_user_funding", "User funding",
		"Read one bounded page of funding payments for the configured account.",
		objectSchema(map[string]any{
			"startTime": unixMillisSchema("inclusive start time"),
			"endTime":   unixMillisSchema("inclusive end time"),
			"limit":     limitSchema(500, 100),
		}, "startTime"),
	), s.userFunding)
	mcp.AddTool(s.mcp, readTool(
		"hl_predicted_funding", "Predicted funding",
		"Read cross-venue funding forecasts for main perpetuals. Optionally filter by one main-DEX symbol.",
		objectSchema(map[string]any{"symbol": stringSchema("main perpetual symbol")}),
	), s.predictedFunding)
	mcp.AddTool(s.mcp, readTool(
		"hl_portfolio", "Portfolio history",
		"Read portfolio value, PnL, and volume periods for the configured account.",
		objectSchema(nil),
	), s.portfolio)
	mcp.AddTool(s.mcp, readTool(
		"hl_fees", "Fee schedule",
		"Read fee rates and volume data for the configured account.",
		objectSchema(nil),
	), s.fees)
	mcp.AddTool(s.mcp, readTool(
		"hl_rate_limit", "Rate-limit usage",
		"Read action-rate-limit usage for the configured account.",
		objectSchema(nil),
	), s.rateLimit)
	mcp.AddTool(s.mcp, readTool(
		"hl_spot_balances", "Spot balances",
		"Read all token balances for the configured account. This is authoritative for unified accounts.",
		objectSchema(nil),
	), s.spotBalances)
	mcp.AddTool(s.mcp, readTool(
		"hl_active_asset_data", "Active asset data",
		"Read current leverage, mark price, available size, and maximum trade sizes for one market.",
		objectSchema(map[string]any{"symbol": stringSchema("configured perpetual symbol")}, "symbol"),
	), s.activeAssetData)
	mcp.AddTool(s.mcp, readTool(
		"hl_mutation_contract", "Mutation contract",
		"Attest the enforced mutation contract: writer-reserved generations, exact-current fence validation, signed Hyperliquid expiresAfter, a five-minute reservation horizon, and a cross-process send lock. Read-only.",
		objectSchema(nil),
	), s.mutationContract)
	mcp.AddTool(s.mcp, mutationTool(
		"hl_reserve_fence", "Reserve mutation fence",
		"Reserve the next writer-owned mutation generation for this account. This non-idempotent call fences every older generation. The expiry must be within five minutes. ownerGeneration is optional audit metadata and never selects the reserved generation.",
		objectSchema(map[string]any{
			"fenceExpiresAtMs": fenceExpirationSchema(),
			"ownerGeneration":  ownerGenerationSchema(),
		}, "fenceExpiresAtMs"), false,
	), s.reserveFence)
	mcp.AddTool(s.mcp, mutationTool(
		"hl_set_leverage", "Set leverage", "Set cross leverage for a symbol before opening a position. Requires the exact current writer-reserved fence and its unexpired reservation deadline.",
		objectSchema(map[string]any{
			"symbol":           stringSchema(""),
			"leverage":         map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			"fenceGeneration":  fenceGenerationSchema(),
			"fenceExpiresAtMs": fenceExpirationSchema(),
		}, "symbol", "leverage", "fenceGeneration", "fenceExpiresAtMs"), false,
	), s.setLeverage)
	mcp.AddTool(s.mcp, mutationTool(
		"hl_place_order", "Place order",
		"Place a limit or market order with the exact current writer-reserved fence and its unexpired reservation deadline, optionally attaching stop-loss and take-profit in one signed action. Omit price for market. Child limit prices are explicit execution bounds; when omitted, the server derives a documented 5% bound from the formatted trigger. Attached orders require their matching client order IDs. MAX_NOTIONAL_USD uses the parent wire price; only verified reduce-only closes may exceed it.",
		placeOrderSchema(), false,
	), s.placeOrder)
	mcp.AddTool(s.mcp, mutationTool(
		"hl_cancel_order", "Cancel order", "Cancel one resting order by id.",
		objectSchema(map[string]any{"id": stringSchema(""), "symbol": stringSchema("")}, "id", "symbol"), true,
	), s.cancelOrder)
	mcp.AddTool(s.mcp, mutationTool(
		"hl_cancel_all", "Cancel all orders", "Cancel every resting order on one symbol.",
		objectSchema(map[string]any{"symbol": stringSchema("")}, "symbol"), false,
	), s.cancelAll)
	mcp.AddTool(s.mcp, readTool(
		"hl_get_trades", "Trade audit log",
		"Read the local SQLite audit log of MCP trade mutations, including reconciliation identity. Results are newest first.",
		objectSchema(map[string]any{
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 50},
			"beforeId":        map[string]any{"type": "integer", "minimum": 1},
			"symbol":          stringSchema("exact symbol"),
			"action":          map[string]any{"type": "string", "enum": []string{"place_order", "set_leverage", "cancel_order", "cancel_all"}},
			"status":          map[string]any{"type": "string", "enum": []string{"pending", "succeeded", "failed", "unknown", "partial"}},
			"includeResponse": map[string]any{"type": "boolean", "default": false},
		}),
	), s.getTrades)
}

func (s *Server) accountIdentity(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.AccountIdentity(ctx)
	return result(value, err)
}

func (s *Server) balance(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.Balance(ctx)
	return result(value, err)
}

func (s *Server) positions(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.Positions(ctx)
	return result(value, err)
}

func (s *Server) ticker(ctx context.Context, _ *mcp.CallToolRequest, input tickerInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.Ticker(ctx, input.Symbol)
	return result(value, err)
}

func (s *Server) searchMarkets(_ context.Context, _ *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, any, error) {
	if input.Limit == 0 {
		input.Limit = 25
	}
	value, err := s.hl.SearchMarkets(input.Query, input.Limit)
	return result(value, err)
}

func (s *Server) openOrders(ctx context.Context, _ *mcp.CallToolRequest, input openOrdersInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.OpenOrders(ctx, input.Symbol)
	return result(value, err)
}

func (s *Server) orderBook(ctx context.Context, _ *mcp.CallToolRequest, input orderBookInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.OrderBook(ctx, hyperliquid.OrderBookParams{
		Symbol: input.Symbol, NSigFigs: input.NSigFigs, Mantissa: input.Mantissa,
	})
	return result(value, err)
}

func (s *Server) candles(ctx context.Context, _ *mcp.CallToolRequest, input candlesInput) (*mcp.CallToolResult, any, error) {
	if input.Limit == 0 {
		input.Limit = 500
	}
	value, err := s.hl.Candles(ctx, hyperliquid.CandlesParams{
		Symbol: input.Symbol, Interval: input.Interval, StartTime: input.StartTime,
		EndTime: input.EndTime, Limit: input.Limit,
	})
	return result(value, err)
}

func (s *Server) userFills(ctx context.Context, _ *mcp.CallToolRequest, input userFillsInput) (*mcp.CallToolResult, any, error) {
	if input.Limit == 0 {
		input.Limit = 100
	}
	value, err := s.hl.UserFills(ctx, hyperliquid.UserFillsParams{
		StartTime: input.StartTime, EndTime: input.EndTime,
		AggregateByTime: input.AggregateByTime, Limit: input.Limit,
	})
	return result(value, err)
}

func (s *Server) orderHistory(ctx context.Context, _ *mcp.CallToolRequest, input orderHistoryInput) (*mcp.CallToolResult, any, error) {
	if input.Limit == 0 {
		input.Limit = 100
	}
	value, err := s.hl.OrderHistory(ctx, input.Limit)
	return result(value, err)
}

func (s *Server) orderStatus(ctx context.Context, _ *mcp.CallToolRequest, input orderStatusInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.OrderStatus(ctx, input.ID)
	return result(value, err)
}

func (s *Server) fundingHistory(ctx context.Context, _ *mcp.CallToolRequest, input fundingHistoryInput) (*mcp.CallToolResult, any, error) {
	if input.Limit == 0 {
		input.Limit = 100
	}
	value, err := s.hl.FundingHistory(ctx, hyperliquid.FundingHistoryParams{
		Symbol: input.Symbol, StartTime: input.StartTime, EndTime: input.EndTime, Limit: input.Limit,
	})
	return result(value, err)
}

func (s *Server) userFunding(ctx context.Context, _ *mcp.CallToolRequest, input userFundingInput) (*mcp.CallToolResult, any, error) {
	if input.Limit == 0 {
		input.Limit = 100
	}
	value, err := s.hl.UserFunding(ctx, hyperliquid.UserFundingParams{
		StartTime: input.StartTime, EndTime: input.EndTime, Limit: input.Limit,
	})
	return result(value, err)
}

func (s *Server) predictedFunding(ctx context.Context, _ *mcp.CallToolRequest, input predictedFundingInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.PredictedFunding(ctx, input.Symbol)
	return result(value, err)
}

func (s *Server) portfolio(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.Portfolio(ctx)
	return result(value, err)
}

func (s *Server) fees(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.Fees(ctx)
	return result(value, err)
}

func (s *Server) rateLimit(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.RateLimit(ctx)
	return result(value, err)
}

func (s *Server) spotBalances(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.SpotBalances(ctx)
	return result(value, err)
}

func (s *Server) activeAssetData(ctx context.Context, _ *mcp.CallToolRequest, input activeAssetDataInput) (*mcp.CallToolResult, any, error) {
	value, err := s.hl.ActiveAssetData(ctx, input.Symbol)
	return result(value, err)
}

func (s *Server) mutationContract(
	_ context.Context, _ *mcp.CallToolRequest, _ emptyInput,
) (*mcp.CallToolResult, any, error) {
	return result(newMutationContract(), nil)
}

func (s *Server) reserveFence(
	ctx context.Context, _ *mcp.CallToolRequest, input reserveFenceInput,
) (*mcp.CallToolResult, any, error) {
	if err := validateFenceExpiration(input.FenceExpiresAtMs); err != nil {
		return failure(err)
	}
	if input.OwnerGeneration != nil && *input.OwnerGeneration <= 0 {
		return failure(errors.New("ownerGeneration must be a positive integer"))
	}
	_, venueKey, err := s.verifiedMutationIdentity(ctx)
	if err != nil {
		return failure(err)
	}
	reservation, err := s.audit.ReserveFence(
		ctx, venueKey, input.FenceExpiresAtMs, input.OwnerGeneration,
	)
	if err != nil {
		return failure(err)
	}
	return result(reserveFenceOutput{
		FenceGeneration: reservation.Generation, FenceExpiresAtMs: reservation.ExpiresAtMs,
		OwnerGeneration: reservation.OwnerGeneration,
	}, nil)
}

func (s *Server) setLeverage(ctx context.Context, _ *mcp.CallToolRequest, input leverageInput) (*mcp.CallToolResult, any, error) {
	s.fencedMutationMu.Lock()
	defer s.fencedMutationMu.Unlock()
	id, options, err := s.startFencedMutation(ctx, audit.StartEvent{
		Action: "set_leverage", Symbol: input.Symbol,
	}, input, input.FenceGeneration, input.FenceExpiresAtMs)
	if err != nil {
		return failure(err)
	}
	mutation, mutationErr := s.runMutation(ctx, func(runCtx context.Context) (hyperliquid.MutationResult, error) {
		return s.hl.SetLeverageWithOptions(runCtx, input.Symbol, input.Leverage, options)
	})
	auditErr := s.completeAudit(id, mutation, mutationErr)
	if mutationErr != nil {
		return failure(joinAuditError(mutationErr, auditErr))
	}
	value := map[string]any{"ok": true, "symbol": input.Symbol, "leverage": input.Leverage}
	addAuditWarning(value, auditErr)
	return result(value, nil)
}

func (s *Server) placeOrder(ctx context.Context, _ *mcp.CallToolRequest, input placeOrderInput) (*mcp.CallToolResult, any, error) {
	s.fencedMutationMu.Lock()
	defer s.fencedMutationMu.Unlock()
	orderType := "limit"
	if input.Price == nil {
		orderType = "market"
	}
	identifiers := make([]audit.OperationIdentifier, 0, 3)
	for _, identifier := range []struct {
		role  string
		value *string
	}{
		{role: "parent", value: input.ParentClientOrderID},
		{role: "takeProfit", value: input.TakeProfitClientOrderID},
		{role: "stopLoss", value: input.StopLossClientOrderID},
	} {
		if identifier.value != nil {
			identifiers = append(identifiers, audit.OperationIdentifier{
				Kind: "cloid", Role: identifier.role, Value: *identifier.value,
			})
		}
	}
	id, options, err := s.startFencedMutation(ctx, audit.StartEvent{
		Action: "place_order", Symbol: input.Symbol, Side: input.Side, OrderType: orderType,
		Amount: &input.Amount, RequestedPrice: input.Price, StopLoss: input.StopLoss,
		TakeProfit: input.TakeProfit, ReduceOnly: input.ReduceOnly,
		OperationIdentifiers: identifiers,
	}, input, input.FenceGeneration, input.FenceExpiresAtMs)
	if err != nil {
		return failure(err)
	}
	mutation, mutationErr := s.runMutation(ctx, func(runCtx context.Context) (hyperliquid.MutationResult, error) {
		return s.hl.PlaceOrderWithOptions(runCtx, hyperliquid.PlaceOrderParams{
			Symbol: input.Symbol, Side: input.Side, Amount: input.Amount, Price: input.Price,
			StopLoss: input.StopLoss, StopLossLimitPrice: input.StopLossLimitPrice,
			TakeProfit: input.TakeProfit, TakeProfitLimitPrice: input.TakeProfitLimitPrice,
			ReduceOnly:              input.ReduceOnly,
			ParentClientOrderID:     input.ParentClientOrderID,
			TakeProfitClientOrderID: input.TakeProfitClientOrderID,
			StopLossClientOrderID:   input.StopLossClientOrderID,
		}, options)
	})
	auditErr := s.completeAudit(id, mutation, mutationErr)
	if mutationErr != nil {
		return failure(joinAuditError(mutationErr, auditErr))
	}
	value := map[string]any{}
	if mutation.Order != nil {
		encoded, _ := json.Marshal(mutation.Order)
		_ = json.Unmarshal(encoded, &value)
	}
	if len(mutation.Attached) != 0 {
		value["attachedOrders"] = mutation.Attached
	}
	addAuditWarning(value, auditErr)
	return result(value, nil)
}

func (s *Server) cancelOrder(ctx context.Context, _ *mcp.CallToolRequest, input cancelOrderInput) (*mcp.CallToolResult, any, error) {
	id, options, err := s.startMutation(ctx, audit.StartEvent{
		Action: "cancel_order", Symbol: input.Symbol, ExchangeOrderID: input.ID,
		OperationIdentifiers: []audit.OperationIdentifier{{Kind: "oid", Value: input.ID}},
	}, input)
	if err != nil {
		return failure(err)
	}
	mutation, mutationErr := s.runMutation(ctx, func(runCtx context.Context) (hyperliquid.MutationResult, error) {
		return s.hl.CancelOrderWithOptions(runCtx, input.ID, input.Symbol, options)
	})
	auditErr := s.completeAudit(id, mutation, mutationErr)
	if mutationErr != nil {
		return failure(joinAuditError(mutationErr, auditErr))
	}
	value := map[string]any{"ok": true, "id": input.ID, "symbol": input.Symbol}
	addAuditWarning(value, auditErr)
	return result(value, nil)
}

func (s *Server) cancelAll(ctx context.Context, _ *mcp.CallToolRequest, input cancelAllInput) (*mcp.CallToolResult, any, error) {
	id, options, err := s.startMutation(ctx, audit.StartEvent{
		Action: "cancel_all", Symbol: input.Symbol,
	}, input)
	if err != nil {
		return failure(err)
	}
	mutation, mutationErr := s.runMutation(ctx, func(runCtx context.Context) (hyperliquid.MutationResult, error) {
		return s.hl.CancelAllWithOptions(runCtx, input.Symbol, options)
	})
	auditErr := s.completeAudit(id, mutation, mutationErr)
	if mutationErr != nil {
		return failure(joinAuditError(mutationErr, auditErr))
	}
	value := map[string]any{"ok": true, "symbol": input.Symbol}
	addAuditWarning(value, auditErr)
	return result(value, nil)
}

func (s *Server) getTrades(ctx context.Context, _ *mcp.CallToolRequest, input getTradesInput) (*mcp.CallToolResult, any, error) {
	value, err := s.audit.List(ctx, audit.Filter{
		Limit: input.Limit, BeforeID: input.BeforeID, Symbol: input.Symbol,
		Action: input.Action, Status: input.Status, IncludeResponse: input.IncludeResponse,
	})
	return result(value, err)
}

type mutationFence struct {
	generation  int64
	expiresAtMs int64
}

func (s *Server) startFencedMutation(
	ctx context.Context, event audit.StartEvent, request any, generation, expiresAtMs int64,
) (int64, hyperliquid.MutationOptions, error) {
	fence := mutationFence{generation: generation, expiresAtMs: expiresAtMs}
	if err := validateMutationFence(fence); err != nil {
		return 0, hyperliquid.MutationOptions{}, err
	}
	return s.startMutationWithFence(ctx, event, request, &fence)
}

func (s *Server) startMutation(
	ctx context.Context, event audit.StartEvent, request any,
) (int64, hyperliquid.MutationOptions, error) {
	return s.startMutationWithFence(ctx, event, request, nil)
}

func (s *Server) startMutationWithFence(
	ctx context.Context, event audit.StartEvent, request any, fence *mutationFence,
) (int64, hyperliquid.MutationOptions, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return 0, hyperliquid.MutationOptions{}, fmt.Errorf("encode audit request: %w", err)
	}
	identity, venueKey, err := s.verifiedMutationIdentity(ctx)
	if err != nil {
		return 0, hyperliquid.MutationOptions{}, err
	}
	event.VenueKey = venueKey
	if fence != nil {
		if err := validateMutationFence(*fence); err != nil {
			return 0, hyperliquid.MutationOptions{}, err
		}
		if err := s.audit.ValidateFence(
			ctx, event.VenueKey, fence.generation, fence.expiresAtMs,
		); err != nil {
			return 0, hyperliquid.MutationOptions{}, err
		}
	}
	nonce := s.hl.ReserveNonce()
	event.OperationIdentifiers = append(event.OperationIdentifiers, audit.OperationIdentifier{
		Kind: "signer", Role: identity.SignerRole, Value: identity.SignerAddress,
	})
	event.Nonce = nonce
	event.Request = encoded
	id, err := s.audit.Start(ctx, event)
	if err != nil {
		return 0, hyperliquid.MutationOptions{}, err
	}
	options := hyperliquid.MutationOptions{Nonce: nonce}
	if fence != nil {
		expiresAfter := uint64(fence.expiresAtMs)
		options.ExpiresAfter = &expiresAfter
	}
	options.BeforeSend = func(sendCtx context.Context, venueRequest json.RawMessage) error {
		current, err := s.hl.AccountIdentity(sendCtx)
		if err != nil {
			return fmt.Errorf("reverify mutation identity: %w", err)
		}
		if !current.AssociationVerified || current.Address != identity.Address ||
			current.SignerAddress != identity.SignerAddress || current.Mainnet != identity.Mainnet {
			return errors.New("mutation identity changed before send")
		}
		if fence != nil {
			if err := validateMutationFence(*fence); err != nil {
				return err
			}
			if err := s.audit.ValidateFence(
				sendCtx, event.VenueKey, fence.generation, fence.expiresAtMs,
			); err != nil {
				return err
			}
		}
		return s.audit.RecordVenueRequest(sendCtx, id, venueRequest)
	}
	if fence != nil {
		options.BeforeNetworkSend = func(sendCtx context.Context) (func(), error) {
			release, err := s.audit.AcquireSendLock(sendCtx)
			if err != nil {
				return nil, err
			}
			if err := validateMutationFence(*fence); err != nil {
				release()
				return nil, err
			}
			if err := s.audit.ValidateFence(
				sendCtx, event.VenueKey, fence.generation, fence.expiresAtMs,
			); err != nil {
				release()
				return nil, err
			}
			return release, nil
		}
	}
	return id, options, nil
}

func (s *Server) verifiedMutationIdentity(
	ctx context.Context,
) (hyperliquid.AccountIdentity, string, error) {
	identity, err := s.hl.AccountIdentity(ctx)
	if err != nil || !identity.AssociationVerified {
		if err == nil {
			err = errors.New("signer-to-account association is not verified")
		}
		return hyperliquid.AccountIdentity{}, "", fmt.Errorf("verify mutation identity: %w", err)
	}
	network := "testnet"
	if identity.Mainnet {
		network = "mainnet"
	}
	venueKey := "hyperliquid:" + network + ":" + strings.ToLower(identity.Address)
	return identity, venueKey, nil
}

func validateMutationFence(fence mutationFence) error {
	if fence.generation <= 0 {
		return errors.New("fenceGeneration must be a positive integer")
	}
	return validateFenceExpiration(fence.expiresAtMs)
}

func validateFenceExpiration(expiresAtMs int64) error {
	now := time.Now()
	if expiresAtMs <= now.UnixMilli() {
		return errors.New("fenceExpiresAtMs must be a future Unix time in milliseconds")
	}
	if expiresAtMs > now.Add(maximumFenceExpiration).UnixMilli() {
		return errors.New("fenceExpiresAtMs must be no more than 5 minutes in the future")
	}
	return nil
}

func (s *Server) runMutation(parent context.Context, call func(context.Context) (hyperliquid.MutationResult, error)) (hyperliquid.MutationResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), s.hl.RequestTimeout())
	defer cancel()
	return call(ctx)
}

func (s *Server) completeAudit(id int64, mutation hyperliquid.MutationResult, mutationErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status := "succeeded"
	if mutationErr != nil {
		status = "failed"
		var typed *hyperliquid.MutationError
		if errors.As(mutationErr, &typed) {
			switch {
			case typed.Ambiguous:
				status = "unknown"
			case typed.Partial:
				status = "partial"
			}
		}
	}
	errorText := ""
	if mutationErr != nil {
		errorText = mutationErr.Error()
	}
	return s.audit.Complete(ctx, id, audit.Completion{
		Status: status, ExchangeOrderID: mutation.ExchangeOrderID,
		Response: mutation.Raw, Error: errorText, Latency: mutation.Latency,
	})
}

func result(value any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return failure(err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return failure(fmt.Errorf("encode tool result: %w", err))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}, nil, nil
}

func failure(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
	}, nil, nil
}

func joinAuditError(actionErr, auditErr error) error {
	if auditErr == nil {
		return actionErr
	}
	return fmt.Errorf("%w; audit completion also failed: %v", actionErr, auditErr)
}

func addAuditWarning(result map[string]any, err error) {
	if err != nil {
		result["auditWarning"] = err.Error()
	}
}

func readTool(name, title, description string, schema any) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Title: title, Description: description, InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: boolPointer(true)},
	}
}

func mutationTool(name, title, description string, schema any, idempotent bool) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Title: title, Description: description, InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{
			Title: title, ReadOnlyHint: false, DestructiveHint: boolPointer(true),
			IdempotentHint: idempotent, OpenWorldHint: boolPointer(true),
		},
	}
}

func placeOrderSchema() map[string]any {
	cloid := func(description string) map[string]any {
		return map[string]any{
			"type": "string", "pattern": `^0x[0-9a-fA-F]{32}$`, "description": description,
		}
	}
	schema := objectSchema(map[string]any{
		"symbol":   stringSchema(""),
		"side":     map[string]any{"type": "string", "enum": []string{"buy", "sell"}},
		"amount":   map[string]any{"type": "number", "exclusiveMinimum": 0},
		"price":    map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "limit price; omit for market"},
		"stopLoss": map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "stop-loss trigger price"},
		"stopLossLimitPrice": map[string]any{
			"type": "number", "exclusiveMinimum": 0,
			"description": "optional stop-loss execution bound; requires stopLoss; at or below the trigger for a sell exit and at or above it for a buy exit",
		},
		"takeProfit": map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "take-profit trigger price"},
		"takeProfitLimitPrice": map[string]any{
			"type": "number", "exclusiveMinimum": 0,
			"description": "optional take-profit execution bound; requires takeProfit; at or below the trigger for a sell exit and at or above it for a buy exit",
		},
		"reduceOnly":              map[string]any{"type": "boolean"},
		"parentClientOrderId":     cloid("optional parent client order ID"),
		"takeProfitClientOrderId": cloid("required exactly when takeProfit is set"),
		"stopLossClientOrderId":   cloid("required exactly when stopLoss is set"),
		"fenceGeneration":         fenceGenerationSchema(),
		"fenceExpiresAtMs":        fenceExpirationSchema(),
	}, "symbol", "side", "amount", "fenceGeneration", "fenceExpiresAtMs")
	schema["allOf"] = []any{
		map[string]any{
			"if":   map[string]any{"required": []string{"takeProfit"}},
			"then": map[string]any{"required": []string{"takeProfitClientOrderId"}},
		},
		map[string]any{
			"if":   map[string]any{"required": []string{"takeProfitClientOrderId"}},
			"then": map[string]any{"required": []string{"takeProfit"}},
		},
		map[string]any{
			"if":   map[string]any{"required": []string{"takeProfitLimitPrice"}},
			"then": map[string]any{"required": []string{"takeProfit"}},
		},
		map[string]any{
			"if":   map[string]any{"required": []string{"stopLoss"}},
			"then": map[string]any{"required": []string{"stopLossClientOrderId"}},
		},
		map[string]any{
			"if":   map[string]any{"required": []string{"stopLossClientOrderId"}},
			"then": map[string]any{"required": []string{"stopLoss"}},
		},
		map[string]any{
			"if":   map[string]any{"required": []string{"stopLossLimitPrice"}},
			"then": map[string]any{"required": []string{"stopLoss"}},
		},
	}
	return schema
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object", "properties": properties, "additionalProperties": false,
	}
	if len(required) != 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	schema := map[string]any{"type": "string", "minLength": 1}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func fenceGenerationSchema() map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 1,
		"description": "exact generation returned by the current hl_reserve_fence call",
	}
}

func fenceExpirationSchema() map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 1,
		"description": "future Unix time in milliseconds, no more than five minutes away; signed as Hyperliquid expiresAfter",
	}
}

func ownerGenerationSchema() map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 1,
		"description": "optional caller generation stored only as reservation audit metadata",
	}
}

func unixMillisSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 0, "description": description}
}

func limitSchema(maximum, defaultValue int) map[string]any {
	return map[string]any{
		"type": "integer", "minimum": 1, "maximum": maximum, "default": defaultValue,
	}
}

func boolPointer(value bool) *bool { return &value }
