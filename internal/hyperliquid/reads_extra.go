package hyperliquid

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var candleIntervals = map[string]struct{}{
	"1m": {}, "3m": {}, "5m": {}, "15m": {}, "30m": {},
	"1h": {}, "2h": {}, "4h": {}, "8h": {}, "12h": {},
	"1d": {}, "3d": {}, "1w": {}, "1M": {},
}

// OrderBookParams controls one l2Book snapshot.
type OrderBookParams struct {
	Symbol   string
	NSigFigs *int
	Mantissa *int
}

// OrderBookLevel is one aggregated level in an order book.
type OrderBookLevel struct {
	Price  float64 `json:"price"`
	Size   float64 `json:"size"`
	Orders int     `json:"orders"`
}

// OrderBook is one bounded Hyperliquid order-book snapshot.
type OrderBook struct {
	Symbol string           `json:"symbol"`
	Time   int64            `json:"time"`
	Bids   []OrderBookLevel `json:"bids"`
	Asks   []OrderBookLevel `json:"asks"`
}

// CandlesParams controls one candleSnapshot request.
type CandlesParams struct {
	Symbol    string
	Interval  string
	StartTime int64
	EndTime   int64
	Limit     int
}

// Candle is one normalized OHLCV candle.
type Candle struct {
	StartTime int64   `json:"startTime"`
	EndTime   int64   `json:"endTime"`
	Symbol    string  `json:"symbol"`
	Interval  string  `json:"interval"`
	Open      float64 `json:"open"`
	Close     float64 `json:"close"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Volume    float64 `json:"volume"`
	Trades    int     `json:"trades"`
}

// UserFillsParams controls a recent or time-bounded userFills request.
type UserFillsParams struct {
	StartTime       *int64
	EndTime         *int64
	AggregateByTime bool
	Limit           int
}

// FundingHistoryParams controls one public fundingHistory request.
type FundingHistoryParams struct {
	Symbol    string
	StartTime int64
	EndTime   *int64
	Limit     int
}

// UserFundingParams controls one userFunding request.
type UserFundingParams struct {
	StartTime int64
	EndTime   *int64
	Limit     int
}

// SpotBalance is one token balance in the configured account.
type SpotBalance struct {
	Coin  string  `json:"coin"`
	Free  float64 `json:"free"`
	Hold  float64 `json:"hold"`
	Total float64 `json:"total"`
}

// ActiveAssetData contains account-specific limits for one market.
type ActiveAssetData struct {
	Symbol           string          `json:"symbol"`
	Leverage         json.RawMessage `json:"leverage"`
	MaxTradeSizes    []float64       `json:"maxTradeSizes"`
	AvailableToTrade []float64       `json:"availableToTrade"`
	MarkPrice        float64         `json:"markPrice"`
}

type orderBookWire struct {
	Coin   string            `json:"coin"`
	Time   int64             `json:"time"`
	Levels [][]bookLevelWire `json:"levels"`
}

type bookLevelWire struct {
	Price  string `json:"px"`
	Size   string `json:"sz"`
	Orders int    `json:"n"`
}

type candleWire struct {
	StartTime int64  `json:"t"`
	EndTime   int64  `json:"T"`
	Coin      string `json:"s"`
	Interval  string `json:"i"`
	Open      string `json:"o"`
	Close     string `json:"c"`
	High      string `json:"h"`
	Low       string `json:"l"`
	Volume    string `json:"v"`
	Trades    int    `json:"n"`
}

type activeAssetDataWire struct {
	Coin             string          `json:"coin"`
	Leverage         json.RawMessage `json:"leverage"`
	MaxTradeSizes    []string        `json:"maxTradeSzs"`
	AvailableToTrade []string        `json:"availableToTrade"`
	MarkPrice        string          `json:"markPx"`
}

// OrderBook returns at most 20 levels on each side for one configured market.
func (c *Client) OrderBook(ctx context.Context, params OrderBookParams) (OrderBook, error) {
	market, err := c.market(params.Symbol)
	if err != nil {
		return OrderBook{}, err
	}
	if err := validateBookAggregation(params.NSigFigs, params.Mantissa); err != nil {
		return OrderBook{}, err
	}
	request := map[string]any{"type": "l2Book", "coin": market.Coin}
	if params.NSigFigs != nil {
		request["nSigFigs"] = *params.NSigFigs
	}
	if params.Mantissa != nil {
		request["mantissa"] = *params.Mantissa
	}
	var wire orderBookWire
	if err := c.info(ctx, request, &wire); err != nil {
		return OrderBook{}, err
	}
	if wire.Coin != "" && wire.Coin != market.Coin {
		return OrderBook{}, fmt.Errorf("l2Book returned coin %q for %q", wire.Coin, market.Coin)
	}
	if len(wire.Levels) != 2 {
		return OrderBook{}, fmt.Errorf("l2Book returned %d sides", len(wire.Levels))
	}
	if len(wire.Levels[0]) > 20 || len(wire.Levels[1]) > 20 {
		return OrderBook{}, errors.New("l2Book returned more than 20 levels on one side")
	}
	bids, err := parseBookLevels(wire.Levels[0])
	if err != nil {
		return OrderBook{}, fmt.Errorf("decode bids: %w", err)
	}
	asks, err := parseBookLevels(wire.Levels[1])
	if err != nil {
		return OrderBook{}, fmt.Errorf("decode asks: %w", err)
	}
	return OrderBook{Symbol: market.Symbol, Time: wire.Time, Bids: bids, Asks: asks}, nil
}

// Candles returns one bounded page of normalized candles for a configured market.
func (c *Client) Candles(ctx context.Context, params CandlesParams) ([]Candle, error) {
	market, err := c.market(params.Symbol)
	if err != nil {
		return nil, err
	}
	if _, ok := candleIntervals[params.Interval]; !ok {
		return nil, fmt.Errorf("unsupported candle interval %q", params.Interval)
	}
	if params.StartTime < 0 || params.EndTime < params.StartTime {
		return nil, errors.New("candle time range is invalid")
	}
	if err := validateLimit(params.Limit, 5000); err != nil {
		return nil, err
	}
	request := map[string]any{
		"type": "candleSnapshot",
		"req": map[string]any{
			"coin": market.Coin, "interval": params.Interval,
			"startTime": params.StartTime, "endTime": params.EndTime,
		},
	}
	var raw []candleWire
	if err := c.info(ctx, request, &raw); err != nil {
		return nil, err
	}
	if len(raw) > params.Limit {
		raw = raw[:params.Limit]
	}
	result := make([]Candle, 0, len(raw))
	for _, item := range raw {
		if item.Coin != "" && item.Coin != market.Coin {
			return nil, fmt.Errorf("candle returned coin %q for %q", item.Coin, market.Coin)
		}
		if item.Interval != params.Interval || item.StartTime < 0 || item.EndTime < item.StartTime || item.Trades < 0 {
			return nil, errors.New("candle response has invalid metadata")
		}
		open, err := parseFinite(item.Open)
		if err != nil {
			return nil, fmt.Errorf("decode candle open: %w", err)
		}
		closeValue, err := parseFinite(item.Close)
		if err != nil {
			return nil, fmt.Errorf("decode candle close: %w", err)
		}
		high, err := parseFinite(item.High)
		if err != nil {
			return nil, fmt.Errorf("decode candle high: %w", err)
		}
		low, err := parseFinite(item.Low)
		if err != nil {
			return nil, fmt.Errorf("decode candle low: %w", err)
		}
		volume, err := parseFinite(item.Volume)
		if err != nil {
			return nil, fmt.Errorf("decode candle volume: %w", err)
		}
		if !finitePositive(open) || !finitePositive(closeValue) || !finitePositive(high) ||
			!finitePositive(low) || volume < 0 {
			return nil, errors.New("candle response has invalid prices or volume")
		}
		result = append(result, Candle{
			StartTime: item.StartTime, EndTime: item.EndTime, Symbol: market.Symbol,
			Interval: item.Interval, Open: open, Close: closeValue, High: high, Low: low,
			Volume: volume, Trades: item.Trades,
		})
	}
	return result, nil
}

// UserFills returns recent fills or one time-bounded page for the configured account.
func (c *Client) UserFills(ctx context.Context, params UserFillsParams) ([]json.RawMessage, error) {
	if err := validateLimit(params.Limit, 2000); err != nil {
		return nil, err
	}
	request := map[string]any{
		"type": "userFills", "user": c.walletAddress,
		"aggregateByTime": params.AggregateByTime,
	}
	if params.StartTime != nil {
		if err := validateTimeRange(*params.StartTime, params.EndTime); err != nil {
			return nil, err
		}
		request["type"] = "userFillsByTime"
		request["startTime"] = *params.StartTime
		if params.EndTime != nil {
			request["endTime"] = *params.EndTime
		}
	} else if params.EndTime != nil {
		return nil, errors.New("endTime requires startTime")
	}
	return c.rawItems(ctx, request, params.Limit)
}

// OrderHistory returns the configured account's most recent historical orders.
func (c *Client) OrderHistory(ctx context.Context, limit int) ([]json.RawMessage, error) {
	if err := validateLimit(limit, 2000); err != nil {
		return nil, err
	}
	return c.rawItems(ctx, map[string]any{
		"type": "historicalOrders", "user": c.walletAddress,
	}, limit)
}

// OrderStatus returns Hyperliquid's status record for an order ID or client order ID.
func (c *Client) OrderStatus(ctx context.Context, id string) (json.RawMessage, error) {
	oid, err := orderLookupID(id)
	if err != nil {
		return nil, err
	}
	return c.rawInfo(ctx, map[string]any{
		"type": "orderStatus", "user": c.walletAddress, "oid": oid,
	})
}

// FundingHistory returns one bounded page of public funding rates for a market.
func (c *Client) FundingHistory(ctx context.Context, params FundingHistoryParams) ([]json.RawMessage, error) {
	market, err := c.market(params.Symbol)
	if err != nil {
		return nil, err
	}
	if err := validateTimeRange(params.StartTime, params.EndTime); err != nil {
		return nil, err
	}
	if err := validateLimit(params.Limit, 500); err != nil {
		return nil, err
	}
	request := map[string]any{
		"type": "fundingHistory", "coin": market.Coin, "startTime": params.StartTime,
	}
	if params.EndTime != nil {
		request["endTime"] = *params.EndTime
	}
	return c.rawItems(ctx, request, params.Limit)
}

// UserFunding returns one bounded page of funding payments for the configured account.
func (c *Client) UserFunding(ctx context.Context, params UserFundingParams) ([]json.RawMessage, error) {
	if err := validateTimeRange(params.StartTime, params.EndTime); err != nil {
		return nil, err
	}
	if err := validateLimit(params.Limit, 500); err != nil {
		return nil, err
	}
	request := map[string]any{
		"type": "userFunding", "user": c.walletAddress, "startTime": params.StartTime,
	}
	if params.EndTime != nil {
		request["endTime"] = *params.EndTime
	}
	return c.rawItems(ctx, request, params.Limit)
}

// PredictedFunding returns cross-venue funding forecasts for main perpetuals.
func (c *Client) PredictedFunding(ctx context.Context, symbol *string) ([]json.RawMessage, error) {
	var filterCoin string
	if symbol != nil {
		market, err := c.market(*symbol)
		if err != nil {
			return nil, err
		}
		if market.DEX != "" {
			return nil, errors.New("predicted funding is available only for the main perpetual DEX")
		}
		filterCoin = market.Coin
	}
	items, err := c.rawItems(ctx, map[string]any{"type": "predictedFundings"}, 10000)
	if err != nil || symbol == nil {
		return items, err
	}
	result := make([]json.RawMessage, 0, 1)
	for _, item := range items {
		var tuple []json.RawMessage
		if err := json.Unmarshal(item, &tuple); err != nil || len(tuple) < 1 {
			return nil, errors.New("decode predicted funding row")
		}
		var coin string
		if err := json.Unmarshal(tuple[0], &coin); err != nil {
			return nil, errors.New("decode predicted funding coin")
		}
		if strings.EqualFold(coin, filterCoin) {
			result = append(result, item)
		}
	}
	return result, nil
}

// Portfolio returns Hyperliquid's portfolio periods for the configured account.
func (c *Client) Portfolio(ctx context.Context) (json.RawMessage, error) {
	return c.rawInfo(ctx, map[string]any{"type": "portfolio", "user": c.walletAddress})
}

// Fees returns the current fee schedule and volume data for the configured account.
func (c *Client) Fees(ctx context.Context) (json.RawMessage, error) {
	return c.rawInfo(ctx, map[string]any{"type": "userFees", "user": c.walletAddress})
}

// RateLimit returns action-rate-limit usage for the configured account.
func (c *Client) RateLimit(ctx context.Context) (json.RawMessage, error) {
	return c.rawInfo(ctx, map[string]any{"type": "userRateLimit", "user": c.walletAddress})
}

// SpotBalances returns all token balances for the configured account.
func (c *Client) SpotBalances(ctx context.Context) ([]SpotBalance, error) {
	var state spotState
	if err := c.info(ctx, map[string]any{
		"type": "spotClearinghouseState", "user": c.walletAddress,
	}, &state); err != nil {
		return nil, err
	}
	result := make([]SpotBalance, 0, len(state.Balances))
	for _, item := range state.Balances {
		total, err := parseFinite(item.Total)
		if err != nil {
			return nil, fmt.Errorf("decode %s total: %w", item.Coin, err)
		}
		hold, err := parseFinite(item.Hold)
		if err != nil {
			return nil, fmt.Errorf("decode %s hold: %w", item.Coin, err)
		}
		result = append(result, SpotBalance{
			Coin: item.Coin, Free: total - hold, Hold: hold, Total: total,
		})
	}
	return result, nil
}

// ActiveAssetData returns account-specific limits and leverage for one market.
func (c *Client) ActiveAssetData(ctx context.Context, symbol string) (ActiveAssetData, error) {
	market, err := c.market(symbol)
	if err != nil {
		return ActiveAssetData{}, err
	}
	var wire activeAssetDataWire
	if err := c.info(ctx, map[string]any{
		"type": "activeAssetData", "user": c.walletAddress, "coin": market.Coin,
	}, &wire); err != nil {
		return ActiveAssetData{}, err
	}
	if wire.Coin != "" && wire.Coin != market.Coin {
		return ActiveAssetData{}, fmt.Errorf("activeAssetData returned coin %q for %q", wire.Coin, market.Coin)
	}
	if len(wire.MaxTradeSizes) != 2 || len(wire.AvailableToTrade) != 2 {
		return ActiveAssetData{}, errors.New("activeAssetData returned invalid size arrays")
	}
	maxTradeSizes, err := parseFiniteValues(wire.MaxTradeSizes)
	if err != nil {
		return ActiveAssetData{}, fmt.Errorf("decode max trade sizes: %w", err)
	}
	available, err := parseFiniteValues(wire.AvailableToTrade)
	if err != nil {
		return ActiveAssetData{}, fmt.Errorf("decode available-to-trade sizes: %w", err)
	}
	markPrice, err := parseFinite(wire.MarkPrice)
	if err != nil || !finitePositive(markPrice) {
		return ActiveAssetData{}, fmt.Errorf("decode mark price: invalid value %q", wire.MarkPrice)
	}
	return ActiveAssetData{
		Symbol: market.Symbol, Leverage: wire.Leverage, MaxTradeSizes: maxTradeSizes,
		AvailableToTrade: available, MarkPrice: markPrice,
	}, nil
}

func (c *Client) rawInfo(ctx context.Context, request map[string]any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.info(ctx, request, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("hyperliquid returned an empty JSON response")
	}
	return raw, nil
}

func (c *Client) rawItems(ctx context.Context, request map[string]any, limit int) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := c.info(ctx, request, &items); err != nil {
		return nil, err
	}
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	return items, nil
}

func validateBookAggregation(nSigFigs, mantissa *int) error {
	if nSigFigs != nil {
		switch *nSigFigs {
		case 2, 3, 4, 5:
		default:
			return errors.New("nSigFigs must be 2, 3, 4, or 5")
		}
	}
	if mantissa == nil {
		return nil
	}
	if nSigFigs == nil || *nSigFigs != 5 {
		return errors.New("mantissa requires nSigFigs equal to 5")
	}
	switch *mantissa {
	case 1, 2, 5:
		return nil
	default:
		return errors.New("mantissa must be 1, 2, or 5")
	}
}

func parseBookLevels(raw []bookLevelWire) ([]OrderBookLevel, error) {
	result := make([]OrderBookLevel, 0, len(raw))
	for _, item := range raw {
		price, err := parseFinite(item.Price)
		if err != nil {
			return nil, err
		}
		size, err := parseFinite(item.Size)
		if err != nil {
			return nil, err
		}
		if !finitePositive(price) || !finitePositive(size) || item.Orders < 0 {
			return nil, errors.New("order-book level has an invalid price, size, or order count")
		}
		result = append(result, OrderBookLevel{Price: price, Size: size, Orders: item.Orders})
	}
	return result, nil
}

func parseFiniteValues(raw []string) ([]float64, error) {
	result := make([]float64, 0, len(raw))
	for _, text := range raw {
		value, err := parseFinite(text)
		if err != nil {
			return nil, err
		}
		if value < 0 {
			return nil, errors.New("size must not be negative")
		}
		result = append(result, value)
	}
	return result, nil
}

func validateTimeRange(startTime int64, endTime *int64) error {
	if startTime < 0 {
		return errors.New("startTime must not be negative")
	}
	if endTime != nil && *endTime < startTime {
		return errors.New("endTime must not be before startTime")
	}
	return nil
}

func validateLimit(limit, maximum int) error {
	if limit < 1 || limit > maximum {
		return fmt.Errorf("limit must be from 1 through %d", maximum)
	}
	return nil
}

func orderLookupID(id string) (any, error) {
	value := strings.TrimSpace(id)
	if oid, err := strconv.ParseUint(value, 10, 64); err == nil {
		return oid, nil
	}
	if len(value) != 34 || !strings.HasPrefix(strings.ToLower(value), "0x") {
		return nil, errors.New("id must be a decimal order ID or 16-byte hexadecimal client order ID")
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return nil, errors.New("id must be a decimal order ID or 16-byte hexadecimal client order ID")
	}
	return strings.ToLower(value), nil
}
