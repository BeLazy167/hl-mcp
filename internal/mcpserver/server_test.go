package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BeLazy167/hl-mcp/internal/audit"
	"github.com/BeLazy167/hl-mcp/internal/hyperliquid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeAPI struct{}

type ambiguousAPI struct{ fakeAPI }

type preSendAuditAPI struct {
	fakeAPI
	store       *audit.Store
	nonce       uint64
	sawPending  bool
	sawPrepared bool
}

func (api *preSendAuditAPI) ReserveNonce() uint64 { return api.nonce }

func (api *preSendAuditAPI) PlaceOrderWithOptions(
	ctx context.Context, params hyperliquid.PlaceOrderParams, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	events, err := api.store.List(ctx, audit.Filter{})
	if err != nil {
		return hyperliquid.MutationResult{}, err
	}
	if len(events) != 1 || events[0].Status != "pending" || events[0].Nonce == nil ||
		*events[0].Nonce != options.Nonce || options.ExpiresAfter == nil ||
		*options.ExpiresAfter <= uint64(time.Now().UnixMilli()) || len(events[0].OperationIdentifiers) != 4 ||
		events[0].VenueKey != "hyperliquid:mainnet:0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		return hyperliquid.MutationResult{}, fmt.Errorf("pre-send audit event = %+v", events)
	}
	api.sawPending = true
	venueRequest := json.RawMessage(`{"type":"order","orders":[{"c":"` + *params.ParentClientOrderID + `"},{"c":"` + *params.TakeProfitClientOrderID + `"},{"c":"` + *params.StopLossClientOrderID + `"}]}`)
	if err := options.BeforeSend(ctx, venueRequest); err != nil {
		return hyperliquid.MutationResult{}, err
	}
	events, err = api.store.List(ctx, audit.Filter{})
	if err != nil {
		return hyperliquid.MutationResult{}, err
	}
	if len(events) != 1 || string(events[0].VenueRequest) != string(venueRequest) {
		return hyperliquid.MutationResult{}, fmt.Errorf("prepared audit event = %+v", events)
	}
	api.sawPrepared = true
	return hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "open", ExchangeOrderID: "123", Raw: json.RawMessage(`{"status":"ok"}`),
		Order: &hyperliquid.PlacedOrder{Status: "open"},
	}, nil
}

func (ambiguousAPI) PlaceOrderWithOptions(
	ctx context.Context, _ hyperliquid.PlaceOrderParams, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	if err := recordFakeVenueRequest(ctx, options, map[string]any{"type": "order"}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "unknown"}, &hyperliquid.MutationError{
		Message:   "exchange outcome unknown; do not retry blindly",
		Ambiguous: true,
	}
}

type serializedFenceAPI struct {
	fakeAPI
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
}

func (api *serializedFenceAPI) SetLeverageWithOptions(
	ctx context.Context, _ string, _ int, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	api.firstOnce.Do(func() { close(api.firstEntered) })
	select {
	case <-ctx.Done():
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, ctx.Err()
	case <-api.releaseFirst:
	}
	if err := recordFakeVenueRequest(ctx, options, map[string]any{"type": "updateLeverage"}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	return hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "succeeded", Raw: json.RawMessage(`{"status":"ok"}`),
	}, nil
}

func (api *serializedFenceAPI) PlaceOrderWithOptions(
	ctx context.Context, params hyperliquid.PlaceOrderParams, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	api.secondOnce.Do(func() { close(api.secondEntered) })
	if err := recordFakeVenueRequest(ctx, options, map[string]any{"type": "order"}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	return hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "open", Raw: json.RawMessage(`{"status":"ok"}`),
		Order: &hyperliquid.PlacedOrder{Symbol: params.Symbol, Status: "open"},
	}, nil
}

type unverifiedAPI struct{ fakeAPI }

func (unverifiedAPI) AccountIdentity(context.Context) (hyperliquid.AccountIdentity, error) {
	return hyperliquid.AccountIdentity{
		Address:                  "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Mainnet:                  true,
		SignerAddress:            "0x1111111111111111111111111111111111111111",
		SignerRole:               "agent",
		AuthorizedAccountAddress: "0x2222222222222222222222222222222222222222",
		AssociationVerified:      false,
	}, nil
}

var fakeNonce atomic.Uint64

func (fakeAPI) ReserveNonce() uint64 { return fakeNonce.Add(1) }

func (fakeAPI) AccountIdentity(context.Context) (hyperliquid.AccountIdentity, error) {
	return hyperliquid.AccountIdentity{
		Address:                  "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Mainnet:                  true,
		SignerAddress:            "0x1111111111111111111111111111111111111111",
		SignerRole:               "agent",
		AuthorizedAccountAddress: "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		AssociationVerified:      true,
	}, nil
}
func (fakeAPI) Balance(context.Context) (hyperliquid.Balance, error) {
	return hyperliquid.Balance{Free: 100, Total: 100}, nil
}
func (fakeAPI) Positions(context.Context) ([]hyperliquid.Position, error) {
	return []hyperliquid.Position{}, nil
}
func (fakeAPI) Ticker(context.Context, string) (hyperliquid.Ticker, error) {
	price := 100.0
	return hyperliquid.Ticker{Symbol: "BTC/USDC:USDC", Last: &price}, nil
}
func (fakeAPI) SearchMarkets(string, int) ([]hyperliquid.MarketSummary, error) {
	return []hyperliquid.MarketSummary{{
		Symbol: "BTC/USDC:USDC", Coin: "BTC", AssetID: 0, SizeDecimals: 3,
		MaxLeverage: 40, MinCostUSD: 10,
	}}, nil
}
func (fakeAPI) OpenOrders(context.Context, *string) ([]hyperliquid.OpenOrder, error) {
	cloid := "0x77777777777777777777777777777777"
	limitPrice, triggerPrice := 95.0, 96.0
	isTrigger, reduceOnly := true, true
	original, remaining, filled := 1.5, 1.0, 0.5
	timestamp := int64(1700000000123)
	return []hyperliquid.OpenOrder{{
		ID: "77", Cloid: &cloid, Symbol: "BTC/USDC:USDC", Side: "sell",
		Type: "market", OrderType: "Stop Market", Price: &limitPrice, LimitPrice: &limitPrice,
		TriggerPrice: &triggerPrice, TriggerCondition: "Price below 96",
		IsTrigger: &isTrigger, ReduceOnly: &reduceOnly, Amount: &original,
		OriginalAmount: &original, RemainingAmount: &remaining, Filled: &filled,
		Timestamp: &timestamp, Status: "open",
	}}, nil
}
func (fakeAPI) OrderBook(context.Context, hyperliquid.OrderBookParams) (hyperliquid.OrderBook, error) {
	return hyperliquid.OrderBook{Symbol: "BTC/USDC:USDC", Bids: []hyperliquid.OrderBookLevel{}, Asks: []hyperliquid.OrderBookLevel{}}, nil
}
func (fakeAPI) Candles(context.Context, hyperliquid.CandlesParams) ([]hyperliquid.Candle, error) {
	return []hyperliquid.Candle{}, nil
}
func (fakeAPI) UserFills(context.Context, hyperliquid.UserFillsParams) ([]json.RawMessage, error) {
	return []json.RawMessage{}, nil
}
func (fakeAPI) OrderHistory(context.Context, int) ([]json.RawMessage, error) {
	return []json.RawMessage{}, nil
}
func (fakeAPI) OrderStatus(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"unknownOid"}`), nil
}
func (fakeAPI) FundingHistory(context.Context, hyperliquid.FundingHistoryParams) ([]json.RawMessage, error) {
	return []json.RawMessage{}, nil
}
func (fakeAPI) UserFunding(context.Context, hyperliquid.UserFundingParams) ([]json.RawMessage, error) {
	return []json.RawMessage{}, nil
}
func (fakeAPI) PredictedFunding(context.Context, *string) ([]json.RawMessage, error) {
	return []json.RawMessage{}, nil
}
func (fakeAPI) Portfolio(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`[]`), nil
}
func (fakeAPI) Fees(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (fakeAPI) RateLimit(context.Context) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (fakeAPI) SpotBalances(context.Context) ([]hyperliquid.SpotBalance, error) {
	return []hyperliquid.SpotBalance{}, nil
}
func (fakeAPI) ActiveAssetData(context.Context, string) (hyperliquid.ActiveAssetData, error) {
	return hyperliquid.ActiveAssetData{Symbol: "BTC/USDC:USDC"}, nil
}
func recordFakeVenueRequest(
	ctx context.Context, options hyperliquid.MutationOptions, request any,
) error {
	if options.BeforeSend == nil {
		return nil
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return options.BeforeSend(ctx, encoded)
}

func (fakeAPI) SetLeverageWithOptions(
	ctx context.Context, _ string, leverage int, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	if err := recordFakeVenueRequest(ctx, options, map[string]any{
		"type": "updateLeverage", "leverage": leverage,
	}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	return hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "succeeded", Raw: json.RawMessage(`{"status":"ok"}`),
	}, nil
}

func (fakeAPI) PlaceOrderWithOptions(
	ctx context.Context, params hyperliquid.PlaceOrderParams, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	if err := recordFakeVenueRequest(ctx, options, map[string]any{
		"type": "order",
		"cloids": []any{
			params.ParentClientOrderID, params.TakeProfitClientOrderID, params.StopLossClientOrderID,
		},
	}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	cloidValue := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	result := hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "open", ExchangeOrderID: "123", Raw: json.RawMessage(`{"status":"ok"}`),
		Order: &hyperliquid.PlacedOrder{
			Role: "parent", Cloid: cloidValue(params.ParentClientOrderID), ID: "123",
			Symbol: params.Symbol, Side: params.Side, Type: "limit", Amount: params.Amount,
			Price: params.Price, Status: "open",
		},
	}
	if params.TakeProfit != nil {
		limit := params.TakeProfitLimitPrice
		if limit == nil {
			derived := *params.TakeProfit * 0.95
			limit = &derived
		}
		result.Attached = append(result.Attached, hyperliquid.OrderStatus{
			Role: "takeProfit", Cloid: cloidValue(params.TakeProfitClientOrderID), Status: "waitingForFill",
			TriggerPrice: params.TakeProfit, LimitPrice: limit,
		})
	}
	if params.StopLoss != nil {
		limit := params.StopLossLimitPrice
		if limit == nil {
			derived := *params.StopLoss * 0.95
			limit = &derived
		}
		result.Attached = append(result.Attached, hyperliquid.OrderStatus{
			Role: "stopLoss", Cloid: cloidValue(params.StopLossClientOrderID), Status: "waitingForTrigger",
			TriggerPrice: params.StopLoss, LimitPrice: limit,
		})
	}
	return result, nil
}

func (fakeAPI) CancelOrderWithOptions(
	ctx context.Context, id, _ string, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	if err := recordFakeVenueRequest(ctx, options, map[string]any{
		"type": "cancel", "id": id,
	}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	return hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "succeeded", Raw: json.RawMessage(`{"status":"ok"}`),
	}, nil
}

func (fakeAPI) CancelAllWithOptions(
	ctx context.Context, symbol string, options hyperliquid.MutationOptions,
) (hyperliquid.MutationResult, error) {
	if err := recordFakeVenueRequest(ctx, options, map[string]any{
		"type": "cancel", "symbol": symbol,
	}); err != nil {
		return hyperliquid.MutationResult{Nonce: options.Nonce, Status: "failed"}, err
	}
	return hyperliquid.MutationResult{
		Nonce: options.Nonce, Status: "succeeded", Raw: json.RawMessage(`{"status":"ok"}`),
	}, nil
}

func (fakeAPI) RequestTimeout() time.Duration { return time.Second }

func futureFenceExpiration() int64 { return time.Now().Add(time.Minute).UnixMilli() }

func reserveFenceForTest(t *testing.T, server *Server) (int64, int64) {
	t.Helper()
	response, _, err := server.reserveFence(context.Background(), nil, reserveFenceInput{
		FenceExpiresAtMs: futureFenceExpiration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError || len(response.Content) != 1 {
		t.Fatalf("reservation response = %+v, err = %v", response, err)
	}
	content, ok := response.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("reservation content type = %T", response.Content[0])
	}
	var output reserveFenceOutput
	if err := json.Unmarshal([]byte(content.Text), &output); err != nil {
		t.Fatalf("reservation payload = %q: %v", content.Text, err)
	}
	if output.FenceGeneration <= 0 || output.FenceExpiresAtMs <= 0 {
		t.Fatalf("reservation output = %+v", output)
	}
	return output.FenceGeneration, output.FenceExpiresAtMs
}

func toolErrorText(t *testing.T, response *mcp.CallToolResult) string {
	t.Helper()
	if response == nil || !response.IsError || len(response.Content) != 1 {
		t.Fatalf("tool response = %+v, want one error", response)
	}
	content, ok := response.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool error content type = %T", response.Content[0])
	}
	return content.Text
}

func TestLatestProtocolAndAuditTool(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	toolServer := New(fakeAPI{}, store)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return toolServer.MCP() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("protocol = %s", got)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 26 {
		t.Fatalf("tools = %d", len(tools.Tools))
	}
	readOnly := map[string]bool{
		"hl_account_identity": true,
		"hl_balance":          true, "hl_positions": true, "hl_ticker": true, "hl_search_markets": true,
		"hl_open_orders": true, "hl_order_book": true, "hl_candles": true, "hl_user_fills": true,
		"hl_order_history": true, "hl_order_status": true, "hl_funding_history": true,
		"hl_user_funding": true, "hl_predicted_funding": true, "hl_portfolio": true,
		"hl_fees": true, "hl_rate_limit": true, "hl_spot_balances": true,
		"hl_active_asset_data": true, "hl_get_trades": true, "hl_mutation_contract": true,
		"hl_reserve_fence": false,
		"hl_set_leverage":  false, "hl_place_order": false, "hl_cancel_order": false, "hl_cancel_all": false,
	}
	for _, tool := range tools.Tools {
		wantReadOnly, ok := readOnly[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != wantReadOnly {
			t.Fatalf("%s readOnlyHint = %+v, want %t", tool.Name, tool.Annotations, wantReadOnly)
		}
		if tool.Name == "hl_cancel_all" && tool.Annotations.IdempotentHint {
			t.Fatalf("hl_cancel_all idempotentHint = true, want false")
		}
		if tool.Name == "hl_set_leverage" || tool.Name == "hl_place_order" {
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties           map[string]json.RawMessage `json:"properties"`
				Required             []string                   `json:"required"`
				AdditionalProperties bool                       `json:"additionalProperties"`
			}
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.AdditionalProperties {
				t.Fatalf("%s permits additional properties", tool.Name)
			}
			required := make(map[string]bool, len(schema.Required))
			for _, name := range schema.Required {
				required[name] = true
			}
			for _, name := range []string{"fenceGeneration", "fenceExpiresAtMs"} {
				raw, ok := schema.Properties[name]
				if !ok || !required[name] {
					t.Fatalf("%s schema does not require %s", tool.Name, name)
				}
				var property struct {
					Type    string `json:"type"`
					Minimum int64  `json:"minimum"`
				}
				if err := json.Unmarshal(raw, &property); err != nil {
					t.Fatal(err)
				}
				if property.Type != "integer" || property.Minimum != 1 {
					t.Fatalf("%s %s schema = %s", tool.Name, name, raw)
				}
			}
			if tool.Name == "hl_place_order" {
				for _, name := range []string{"takeProfitLimitPrice", "stopLossLimitPrice"} {
					if _, ok := schema.Properties[name]; !ok {
						t.Fatalf("hl_place_order schema missing %s", name)
					}
				}
			}
		}
		delete(readOnly, tool.Name)
	}
	if len(readOnly) != 0 {
		t.Fatalf("missing tools: %v", readOnly)
	}

	readCalls := []struct {
		name      string
		arguments map[string]any
	}{
		{"hl_account_identity", map[string]any{}},
		{"hl_order_book", map[string]any{"symbol": "BTC/USDC:USDC"}},
		{"hl_candles", map[string]any{"symbol": "BTC/USDC:USDC", "interval": "1m", "startTime": 0, "endTime": 60_000}},
		{"hl_user_fills", map[string]any{}},
		{"hl_order_history", map[string]any{}},
		{"hl_order_status", map[string]any{"id": "1"}},
		{"hl_funding_history", map[string]any{"symbol": "BTC/USDC:USDC", "startTime": 0}},
		{"hl_user_funding", map[string]any{"startTime": 0}},
		{"hl_predicted_funding", map[string]any{}},
		{"hl_portfolio", map[string]any{}},
		{"hl_fees", map[string]any{}},
		{"hl_rate_limit", map[string]any{}},
		{"hl_spot_balances", map[string]any{}},
		{"hl_active_asset_data", map[string]any{"symbol": "BTC/USDC:USDC"}},
	}
	for _, call := range readCalls {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: call.name, Arguments: call.arguments,
		})
		if err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		if result.IsError {
			t.Fatalf("%s result: %+v", call.name, result)
		}
	}

	decodeTool := func(name string, arguments map[string]any, output any) {
		t.Helper()
		callResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: name, Arguments: arguments,
		})
		if err != nil {
			t.Fatal(err)
		}
		if callResult.IsError || len(callResult.Content) != 1 {
			t.Fatalf("%s result: %+v", name, callResult)
		}
		content, ok := callResult.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("%s content type = %T", name, callResult.Content[0])
		}
		if err := json.Unmarshal([]byte(content.Text), output); err != nil {
			t.Fatal(err)
		}
	}
	var identity map[string]any
	decodeTool("hl_account_identity", map[string]any{}, &identity)
	if identity["address"] != "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" || identity["mainnet"] != true ||
		identity["associationVerified"] != true ||
		identity["authorizedAccountAddress"] != "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" || len(identity) != 6 {
		t.Fatalf("identity = %#v", identity)
	}

	var markets []hyperliquid.MarketSummary
	decodeTool("hl_search_markets", map[string]any{"query": "BTC"}, &markets)
	if len(markets) != 1 || markets[0].Coin != "BTC" || markets[0].DEX != "" ||
		markets[0].AssetID != 0 || markets[0].SizeDecimals != 3 || markets[0].MaxLeverage != 40 ||
		markets[0].MinCostUSD != 10 {
		t.Fatalf("markets = %+v", markets)
	}
	var openOrders []hyperliquid.OpenOrder
	decodeTool("hl_open_orders", map[string]any{}, &openOrders)
	if len(openOrders) != 1 || openOrders[0].Cloid == nil ||
		*openOrders[0].Cloid != "0x77777777777777777777777777777777" ||
		openOrders[0].LimitPrice == nil || *openOrders[0].LimitPrice != 95 ||
		openOrders[0].TriggerPrice == nil || *openOrders[0].TriggerPrice != 96 ||
		openOrders[0].TriggerCondition != "Price below 96" || openOrders[0].IsTrigger == nil ||
		!*openOrders[0].IsTrigger || openOrders[0].ReduceOnly == nil || !*openOrders[0].ReduceOnly ||
		openOrders[0].OriginalAmount == nil || *openOrders[0].OriginalAmount != 1.5 ||
		openOrders[0].RemainingAmount == nil || *openOrders[0].RemainingAmount != 1 ||
		openOrders[0].OrderType != "Stop Market" || openOrders[0].Timestamp == nil ||
		*openOrders[0].Timestamp != 1700000000123 {
		t.Fatalf("open orders = %+v", openOrders)
	}

	missingFence, missingFenceErr := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_set_leverage",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "leverage": 5,
		},
	})
	if missingFenceErr == nil && (missingFence == nil || !missingFence.IsError) {
		t.Fatalf("missing evidence fence passed schema validation: %+v", missingFence)
	}
	wrongFenceType, wrongFenceTypeErr := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_set_leverage",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "leverage": 5,
			"fenceGeneration": 1.5, "fenceExpiresAtMs": futureFenceExpiration(),
		},
	})
	if wrongFenceTypeErr == nil && (wrongFenceType == nil || !wrongFenceType.IsError) {
		t.Fatalf("fractional evidence fence passed schema validation: %+v", wrongFenceType)
	}

	price, stop, take := 100.0, 90.0, 110.0
	fenceExpiresAtMs := futureFenceExpiration()
	invalid, invalidErr := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_place_order",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "side": "buy", "amount": 0.1, "price": price,
			"fenceGeneration": 1, "fenceExpiresAtMs": fenceExpiresAtMs,
			"takeProfit": take,
		},
	})
	if invalidErr == nil && (invalid == nil || !invalid.IsError) {
		t.Fatalf("missing child cloid passed schema validation: %+v", invalid)
	}
	invalid, invalidErr = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_place_order",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "side": "buy", "amount": 0.1, "price": price,
			"fenceGeneration": 1, "fenceExpiresAtMs": fenceExpiresAtMs,
			"parentClientOrderId": "0X11111111111111111111111111111111",
		},
	})
	if invalidErr == nil && (invalid == nil || !invalid.IsError) {
		t.Fatalf("malformed parent cloid passed schema validation: %+v", invalid)
	}
	invalid, invalidErr = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_place_order",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "side": "buy", "amount": 0.1, "price": price,
			"fenceGeneration": 1, "fenceExpiresAtMs": fenceExpiresAtMs,
			"takeProfitLimitPrice": 108.25,
		},
	})
	if invalidErr == nil && (invalid == nil || !invalid.IsError) {
		t.Fatalf("unpaired child limit passed schema validation: %+v", invalid)
	}

	generation, reservedExpiresAtMs := reserveFenceForTest(t, toolServer)
	parentCloid := "0x11111111111111111111111111111111"
	takeCloid := "0x22222222222222222222222222222222"
	stopCloid := "0x33333333333333333333333333333333"
	takeLimit, stopLimit := 108.25, 88.75
	placed, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_place_order",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "side": "buy", "amount": 0.1, "price": price,
			"fenceGeneration": generation, "fenceExpiresAtMs": reservedExpiresAtMs,
			"stopLoss": stop, "stopLossLimitPrice": stopLimit,
			"takeProfit": take, "takeProfitLimitPrice": takeLimit,
			"parentClientOrderId":     parentCloid,
			"takeProfitClientOrderId": takeCloid,
			"stopLossClientOrderId":   stopCloid,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if placed.IsError || len(placed.Content) != 1 {
		t.Fatalf("place result: %+v", placed)
	}
	placedText, ok := placed.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("place content type = %T", placed.Content[0])
	}
	var placedValue map[string]any
	if err := json.Unmarshal([]byte(placedText.Text), &placedValue); err != nil {
		t.Fatal(err)
	}
	if placedValue["role"] != "parent" || placedValue["cloid"] != parentCloid {
		t.Fatalf("placed order = %#v", placedValue)
	}
	attached, ok := placedValue["attachedOrders"].([]any)
	if !ok || len(attached) != 2 {
		t.Fatalf("attached orders = %#v", placedValue["attachedOrders"])
	}
	for index, expected := range []struct {
		role, cloid, status string
		trigger, limit      float64
	}{
		{role: "takeProfit", cloid: takeCloid, status: "waitingForFill", trigger: 110, limit: takeLimit},
		{role: "stopLoss", cloid: stopCloid, status: "waitingForTrigger", trigger: 90, limit: stopLimit},
	} {
		order, ok := attached[index].(map[string]any)
		if !ok || order["role"] != expected.role || order["cloid"] != expected.cloid ||
			order["status"] != expected.status || order["triggerPrice"] != expected.trigger ||
			order["limitPrice"] != expected.limit {
			t.Fatalf("attached order %d = %#v", index, attached[index])
		}
		if _, exists := order["price"]; exists {
			t.Fatalf("attached order has ambiguous price: %#v", order)
		}
	}

	listed, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_get_trades", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if listed.IsError || len(listed.Content) != 1 {
		t.Fatalf("audit result: %+v", listed)
	}
	text, ok := listed.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", listed.Content[0])
	}
	var events []audit.Event
	if err := json.Unmarshal([]byte(text.Text), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "place_order" || events[0].Status != "succeeded" {
		t.Fatalf("events = %+v", events)
	}
}

func TestLegacyStatelessProtocol(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	toolServer := New(fakeAPI{}, store)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return toolServer.MCP() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	call := func(payload string) map[string]any {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, httpServer.URL, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", response.StatusCode)
		}
		var value map[string]any
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	initialized := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-test","version":"1"}}}`)
	result, ok := initialized["result"].(map[string]any)
	if !ok || result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize = %#v", initialized)
	}
	listed := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	listResult, ok := listed["result"].(map[string]any)
	tools, toolsOK := listResult["tools"].([]any)
	if !ok || !toolsOK || len(tools) != 26 {
		t.Fatalf("tools/list = %#v", listed)
	}
}

func TestMutationRejectsUnverifiedSignerBeforeAuditOrSend(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := New(unverifiedAPI{}, store)
	response, _, err := server.setLeverage(context.Background(), nil, leverageInput{
		Symbol: "BTC/USDC:USDC", Leverage: 5,
		FenceGeneration: 1, FenceExpiresAtMs: futureFenceExpiration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError {
		t.Fatalf("response = %+v; want error", response)
	}
	events, err := store.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v; want no mutation audit because no send was possible", events)
	}
}

func TestFencedMutationsRejectInvalidOrExpiredFence(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation int64
		expires    int64
		wantError  string
	}{
		{name: "missing generation", generation: 0, expires: futureFenceExpiration(), wantError: "fenceGeneration"},
		{name: "expired", generation: 1, expires: time.Now().Add(-time.Millisecond).UnixMilli(), wantError: "fenceExpiresAtMs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server := New(fakeAPI{}, store)
			response, _, err := server.setLeverage(context.Background(), nil, leverageInput{
				Symbol: "BTC/USDC:USDC", Leverage: 5,
				FenceGeneration: test.generation, FenceExpiresAtMs: test.expires,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := toolErrorText(t, response); !strings.Contains(got, test.wantError) {
				t.Fatalf("error = %q, want %q", got, test.wantError)
			}
			events, err := store.List(context.Background(), audit.Filter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Fatalf("events = %+v, want none", events)
			}
		})
	}
}

func TestFencedMutationsAllowSameGenerationReuseButRejectStaleReservation(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := New(fakeAPI{}, store)
	firstGeneration, expiresAtMs := reserveFenceForTest(t, server)

	response, _, err := server.setLeverage(context.Background(), nil, leverageInput{
		Symbol: "BTC/USDC:USDC", Leverage: 5,
		FenceGeneration: firstGeneration, FenceExpiresAtMs: expiresAtMs,
	})
	if err != nil || response.IsError {
		t.Fatalf("initial leverage response = %+v, err = %v", response, err)
	}
	price := 100.0
	response, _, err = server.placeOrder(context.Background(), nil, placeOrderInput{
		Symbol: "BTC/USDC:USDC", Side: "buy", Amount: 0.1, Price: &price,
		FenceGeneration: firstGeneration, FenceExpiresAtMs: expiresAtMs,
	})
	if err != nil || response.IsError {
		t.Fatalf("same-generation order response = %+v, err = %v", response, err)
	}
	secondGeneration, secondExpiresAtMs := reserveFenceForTest(t, server)
	response, _, err = server.setLeverage(context.Background(), nil, leverageInput{
		Symbol: "BTC/USDC:USDC", Leverage: 6,
		FenceGeneration: firstGeneration, FenceExpiresAtMs: expiresAtMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolErrorText(t, response); !strings.Contains(got, "mutation fence generation is not current") {
		t.Fatalf("stale error = %q", got)
	}
	response, _, err = server.placeOrder(context.Background(), nil, placeOrderInput{
		Symbol: "BTC/USDC:USDC", Side: "buy", Amount: 0.1, Price: &price,
		FenceGeneration: secondGeneration, FenceExpiresAtMs: secondExpiresAtMs,
	})
	if err != nil || response.IsError {
		t.Fatalf("new-generation order response = %+v, err = %v", response, err)
	}

	events, err := store.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want three authorized mutations", events)
	}
}

func TestNewerReservationOnSecondServerFencesOlderServer(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	older := New(fakeAPI{}, store)
	newer := New(fakeAPI{}, store)
	olderGeneration, _ := reserveFenceForTest(t, older)
	newerGeneration, newerExpiresAtMs := reserveFenceForTest(t, newer)
	if newerGeneration <= olderGeneration {
		t.Fatalf("generations = %d then %d, want strictly increasing", olderGeneration, newerGeneration)
	}

	response, _, err := older.setLeverage(context.Background(), nil, leverageInput{
		Symbol: "BTC/USDC:USDC", Leverage: 5,
		FenceGeneration: olderGeneration, FenceExpiresAtMs: futureFenceExpiration(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolErrorText(t, response); !strings.Contains(got, "mutation fence generation is not current") {
		t.Fatalf("older server error = %q, want stale fence rejection", got)
	}
	response, _, err = newer.setLeverage(context.Background(), nil, leverageInput{
		Symbol: "BTC/USDC:USDC", Leverage: 5,
		FenceGeneration: newerGeneration, FenceExpiresAtMs: newerExpiresAtMs,
	})
	if err != nil || response.IsError {
		t.Fatalf("newer server response = %+v, err = %v", response, err)
	}

	events, err := store.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want only the newer server mutation", events)
	}
}

func TestCancelsRemainAvailableWithoutFence(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := New(fakeAPI{}, store)
	response, _, err := server.cancelOrder(context.Background(), nil, cancelOrderInput{
		ID: "42", Symbol: "BTC/USDC:USDC",
	})
	if err != nil || response.IsError {
		t.Fatalf("cancel order response = %+v, err = %v", response, err)
	}
	response, _, err = server.cancelAll(context.Background(), nil, cancelAllInput{
		Symbol: "BTC/USDC:USDC",
	})
	if err != nil || response.IsError {
		t.Fatalf("cancel all response = %+v, err = %v", response, err)
	}
}

func TestFencedMutationHandlersAreSerializedThroughSend(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &serializedFenceAPI{
		firstEntered: make(chan struct{}), releaseFirst: make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
	server := New(api, store)
	generation, expiresAtMs := reserveFenceForTest(t, server)
	firstDone := make(chan *mcp.CallToolResult, 1)
	go func() {
		response, _, _ := server.setLeverage(context.Background(), nil, leverageInput{
			Symbol: "BTC/USDC:USDC", Leverage: 5,
			FenceGeneration: generation, FenceExpiresAtMs: expiresAtMs,
		})
		firstDone <- response
	}()
	select {
	case <-api.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first mutation did not reach the authorized send path")
	}

	secondDone := make(chan *mcp.CallToolResult, 1)
	price := 100.0
	go func() {
		response, _, _ := server.placeOrder(context.Background(), nil, placeOrderInput{
			Symbol: "BTC/USDC:USDC", Side: "buy", Amount: 0.1, Price: &price,
			FenceGeneration: generation, FenceExpiresAtMs: expiresAtMs,
		})
		secondDone <- response
	}()
	select {
	case <-api.secondEntered:
		t.Fatal("second mutation overtook an in-flight first mutation")
	case <-time.After(50 * time.Millisecond):
	}
	close(api.releaseFirst)
	for name, done := range map[string]<-chan *mcp.CallToolResult{
		"first": firstDone, "second": secondDone,
	} {
		select {
		case response := <-done:
			if response == nil || response.IsError {
				t.Fatalf("%s response = %+v", name, response)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s mutation did not complete", name)
		}
	}
}

func TestPlaceOrderPersistsNonceAndCloidsBeforeSend(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &preSendAuditAPI{store: store, nonce: 1700000000456}
	server := New(api, store)
	generation, expiresAtMs := reserveFenceForTest(t, server)
	price, takeProfit, stopLoss := 100.0, 110.0, 90.0
	parentCloid := "0x11111111111111111111111111111111"
	takeCloid := "0x22222222222222222222222222222222"
	stopCloid := "0x33333333333333333333333333333333"
	response, _, err := server.placeOrder(context.Background(), nil, placeOrderInput{
		Symbol: "BTC/USDC:USDC", Side: "buy", Amount: 0.1, Price: &price,
		TakeProfit: &takeProfit, StopLoss: &stopLoss,
		ParentClientOrderID: &parentCloid, TakeProfitClientOrderID: &takeCloid,
		StopLossClientOrderID: &stopCloid,
		FenceGeneration:       generation, FenceExpiresAtMs: expiresAtMs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError || !api.sawPending || !api.sawPrepared {
		t.Fatalf("response = %+v, pending = %t, prepared = %t", response, api.sawPending, api.sawPrepared)
	}
	events, err := store.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Nonce == nil || *events[0].Nonce != api.nonce ||
		len(events[0].OperationIdentifiers) != 4 || len(events[0].Request) == 0 ||
		len(events[0].VenueRequest) == 0 {
		t.Fatalf("completed audit event = %+v", events)
	}
}

func TestAmbiguousMutationIsAuditedUnknown(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	toolServer := New(ambiguousAPI{}, store)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return toolServer.MCP() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	generation, expiresAtMs := reserveFenceForTest(t, toolServer)
	price := 100.0
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hl_place_order",
		Arguments: map[string]any{
			"symbol": "BTC/USDC:USDC", "side": "buy", "amount": 0.1, "price": price,
			"fenceGeneration": generation, "fenceExpiresAtMs": expiresAtMs,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v", result)
	}
	events, err := store.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Status != "unknown" || events[0].Nonce == nil || *events[0].Nonce == 0 {
		t.Fatalf("events = %+v", events)
	}
}

func TestMutationContractMatchesShippedFixture(t *testing.T) {
	contract := newMutationContract()
	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "mutation_contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shipped any
	if err := json.Unmarshal(fixture, &shipped); err != nil {
		t.Fatal(err)
	}
	var actual any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(actual) != fmt.Sprint(shipped) {
		t.Fatalf("contract = %s, fixture = %s", encoded, fixture)
	}
	if contract.Fence.MaxExpirationMs != 300000 {
		t.Fatalf("max expiration = %d, want 300000", contract.Fence.MaxExpirationMs)
	}
}

func TestReservationWaitsForHeldSendLock(t *testing.T) {
	store, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	release, err := store.AcquireSendLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := store.ReserveFence(ctx, "venue", futureFenceExpiration(), nil); err == nil {
		t.Fatal("reservation must not commit while a send holds the lock")
	}
	release()
	reservation, err := store.ReserveFence(
		context.Background(), "venue", futureFenceExpiration(), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Generation != 1 {
		t.Fatalf("generation = %d, want 1", reservation.Generation)
	}
}
