package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

type Balance struct {
	Free  float64 `json:"free"`
	Used  float64 `json:"used"`
	Total float64 `json:"total"`
}

type Position struct {
	Symbol           string   `json:"symbol"`
	Side             string   `json:"side"`
	Contracts        float64  `json:"contracts"`
	EntryPrice       *float64 `json:"entryPrice,omitempty"`
	MarkPrice        *float64 `json:"markPrice,omitempty"`
	LiquidationPrice *float64 `json:"liquidationPrice,omitempty"`
	Leverage         *float64 `json:"leverage,omitempty"`
	UnrealizedPnL    *float64 `json:"unrealizedPnl,omitempty"`
}

type Ticker struct {
	Symbol string   `json:"symbol"`
	Last   *float64 `json:"last,omitempty"`
	Bid    *float64 `json:"bid,omitempty"`
	Ask    *float64 `json:"ask,omitempty"`
}

type OpenOrder struct {
	ID               string   `json:"id"`
	Cloid            *string  `json:"cloid,omitempty"`
	Symbol           string   `json:"symbol"`
	Side             string   `json:"side,omitempty"`
	Type             string   `json:"type,omitempty"`
	OrderType        string   `json:"orderType,omitempty"`
	Price            *float64 `json:"price,omitempty"`
	LimitPrice       *float64 `json:"limitPrice,omitempty"`
	TriggerPrice     *float64 `json:"triggerPrice,omitempty"`
	TriggerCondition string   `json:"triggerCondition,omitempty"`
	IsTrigger        *bool    `json:"isTrigger,omitempty"`
	ReduceOnly       *bool    `json:"reduceOnly,omitempty"`
	Amount           *float64 `json:"amount,omitempty"`
	OriginalAmount   *float64 `json:"originalAmount,omitempty"`
	RemainingAmount  *float64 `json:"remainingAmount,omitempty"`
	Filled           *float64 `json:"filled,omitempty"`
	Timestamp        *int64   `json:"timestamp,omitempty"`
	Status           string   `json:"status"`
}

type clearinghouseState struct {
	AssetPositions []struct {
		Position struct {
			Coin             string  `json:"coin"`
			EntryPrice       *string `json:"entryPx"`
			LiquidationPrice *string `json:"liquidationPx"`
			PositionValue    string  `json:"positionValue"`
			Size             string  `json:"szi"`
			UnrealizedPnL    string  `json:"unrealizedPnl"`
			Leverage         struct {
				Value json.RawMessage `json:"value"`
			} `json:"leverage"`
		} `json:"position"`
	} `json:"assetPositions"`
	MarginSummary struct {
		AccountValue    string `json:"accountValue"`
		TotalMarginUsed string `json:"totalMarginUsed"`
	} `json:"marginSummary"`
}

type spotState struct {
	Balances []struct {
		Coin  string `json:"coin"`
		Hold  string `json:"hold"`
		Total string `json:"total"`
	} `json:"balances"`
}

type assetContext struct {
	ImpactPrices []string `json:"impactPxs"`
	MarkPrice    string   `json:"markPx"`
	MidPrice     *string  `json:"midPx"`
}

type frontendOrder struct {
	Coin             string          `json:"coin"`
	Cloid            *string         `json:"cloid"`
	LimitPx          string          `json:"limitPx"`
	TriggerPx        string          `json:"triggerPx"`
	TriggerCondition string          `json:"triggerCondition"`
	IsTrigger        *bool           `json:"isTrigger"`
	ReduceOnly       *bool           `json:"reduceOnly"`
	OID              json.RawMessage `json:"oid"`
	OrderType        string          `json:"orderType"`
	OrigSize         string          `json:"origSz"`
	Side             string          `json:"side"`
	Size             string          `json:"sz"`
	Timestamp        *int64          `json:"timestamp"`
}

func (c *Client) Balance(ctx context.Context) (Balance, error) {
	c.accountMu.RLock()
	mode := c.accountMode
	c.accountMu.RUnlock()
	if mode == "unifiedAccount" {
		var state spotState
		if err := c.info(ctx, map[string]any{
			"type": "spotClearinghouseState", "user": c.walletAddress,
		}, &state); err != nil {
			return Balance{}, err
		}
		for _, balance := range state.Balances {
			if balance.Coin != "USDC" {
				continue
			}
			total, err := parseFinite(balance.Total)
			if err != nil {
				return Balance{}, fmt.Errorf("decode USDC total: %w", err)
			}
			used, err := parseFinite(balance.Hold)
			if err != nil {
				return Balance{}, fmt.Errorf("decode USDC hold: %w", err)
			}
			return Balance{Free: total - used, Used: used, Total: total}, nil
		}
		return Balance{}, nil
	}

	state, err := c.userState(ctx, "")
	if err != nil {
		return Balance{}, err
	}
	total, err := parseFinite(state.MarginSummary.AccountValue)
	if err != nil {
		return Balance{}, fmt.Errorf("decode account value: %w", err)
	}
	used, err := parseFinite(state.MarginSummary.TotalMarginUsed)
	if err != nil {
		return Balance{}, fmt.Errorf("decode margin used: %w", err)
	}
	return Balance{Free: total - used, Used: used, Total: total}, nil
}

func (c *Client) Positions(ctx context.Context) ([]Position, error) {
	dexes := append([]string{""}, c.dexes...)
	states := make([]clearinghouseState, len(dexes))
	errs := make([]error, len(dexes))
	var wait sync.WaitGroup
	wait.Add(len(dexes))
	for index, dex := range dexes {
		go func() {
			defer wait.Done()
			states[index], errs[index] = c.userState(ctx, dex)
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("read positions for DEX %q: %w", dexes[index], err)
		}
	}

	var result []Position
	for _, state := range states {
		for _, wrapper := range state.AssetPositions {
			position := wrapper.Position
			size, err := parseFinite(position.Size)
			if err != nil {
				return nil, fmt.Errorf("decode %s position size: %w", position.Coin, err)
			}
			if size == 0 {
				continue
			}
			market, err := c.market(position.Coin)
			if err != nil {
				return nil, err
			}
			side := "long"
			if size < 0 {
				side = "short"
			}
			result = append(result, Position{
				Symbol:           market.Symbol,
				Side:             side,
				Contracts:        math.Abs(size),
				EntryPrice:       parseOptionalString(position.EntryPrice),
				MarkPrice:        positionMarkPrice(position.PositionValue, size),
				LiquidationPrice: parseOptionalString(position.LiquidationPrice),
				Leverage:         parseOptionalRaw(position.Leverage.Value),
				UnrealizedPnL:    parseOptionalValue(position.UnrealizedPnL),
			})
		}
	}
	if result == nil {
		result = []Position{}
	}
	return result, nil
}

func (c *Client) Ticker(ctx context.Context, symbol string) (Ticker, error) {
	market, err := c.market(symbol)
	if err != nil {
		return Ticker{}, err
	}
	_, contexts, err := c.metaAndContexts(ctx, market.DEX)
	if err != nil {
		return Ticker{}, err
	}
	if market.LocalIndex < 0 || market.LocalIndex >= len(contexts) {
		return Ticker{}, errors.New("market context index is outside the returned metadata")
	}
	var value assetContext
	if err := json.Unmarshal(contexts[market.LocalIndex], &value); err != nil {
		return Ticker{}, fmt.Errorf("decode ticker: %w", err)
	}
	result := Ticker{Symbol: market.Symbol}
	if value.MidPrice != nil {
		result.Last = parseOptionalValue(*value.MidPrice)
	} else {
		result.Last = parseOptionalValue(value.MarkPrice)
	}
	if len(value.ImpactPrices) >= 2 {
		result.Bid = parseOptionalValue(value.ImpactPrices[0])
		result.Ask = parseOptionalValue(value.ImpactPrices[1])
	}
	if result.Last == nil {
		return Ticker{}, errors.New("hyperliquid returned no usable midpoint")
	}
	return result, nil
}

func (c *Client) OpenOrders(ctx context.Context, symbol *string) ([]OpenOrder, error) {
	if symbol != nil {
		market, err := c.market(*symbol)
		if err != nil {
			return nil, err
		}
		orders, err := c.openOrdersForDEX(ctx, market.DEX)
		if err != nil {
			return nil, err
		}
		return c.parseOpenOrders(orders, &market)
	}

	dexes := append([]string{""}, c.dexes...)
	groups := make([][]frontendOrder, len(dexes))
	errs := make([]error, len(dexes))
	var wait sync.WaitGroup
	wait.Add(len(dexes))
	for index, dex := range dexes {
		go func() {
			defer wait.Done()
			groups[index], errs[index] = c.openOrdersForDEX(ctx, dex)
		}()
	}
	wait.Wait()
	var result []OpenOrder
	for index, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("read open orders for DEX %q: %w", dexes[index], err)
		}
		parsed, err := c.parseOpenOrders(groups[index], nil)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed...)
	}
	if result == nil {
		result = []OpenOrder{}
	}
	return result, nil
}

func (c *Client) userState(ctx context.Context, dex string) (clearinghouseState, error) {
	request := map[string]any{"type": "clearinghouseState", "user": c.walletAddress}
	if dex != "" {
		request["dex"] = dex
	}
	var state clearinghouseState
	err := c.info(ctx, request, &state)
	return state, err
}

func (c *Client) openOrdersForDEX(ctx context.Context, dex string) ([]frontendOrder, error) {
	request := map[string]any{"type": "frontendOpenOrders", "user": c.walletAddress}
	if dex != "" {
		request["dex"] = dex
	}
	var orders []frontendOrder
	if err := c.info(ctx, request, &orders); err != nil {
		return nil, err
	}
	return orders, nil
}

func (c *Client) parseOpenOrders(raw []frontendOrder, filter *Market) ([]OpenOrder, error) {
	result := make([]OpenOrder, 0, len(raw))
	for _, order := range raw {
		market, err := c.market(order.Coin)
		if err != nil {
			return nil, err
		}
		if filter != nil && market.Coin != filter.Coin {
			continue
		}
		id, err := parseOID(order.OID)
		if err != nil {
			return nil, fmt.Errorf("decode order id: %w", err)
		}
		var side string
		switch order.Side {
		case "B":
			side = "buy"
		case "A":
			side = "sell"
		default:
			return nil, fmt.Errorf("decode order %s side %q", id, order.Side)
		}
		orderType := strings.ToLower(order.OrderType)
		switch {
		case strings.Contains(orderType, "market"):
			orderType = "market"
		case strings.Contains(orderType, "limit"):
			orderType = "limit"
		}
		original, err := parseOrderNumber(order.OrigSize, "original amount", id)
		if err != nil {
			return nil, err
		}
		remaining, err := parseOrderNumber(order.Size, "remaining amount", id)
		if err != nil {
			return nil, err
		}
		limitPrice, err := parseOrderNumber(order.LimitPx, "limit price", id)
		if err != nil {
			return nil, err
		}
		triggerPrice, err := parseOrderNumber(order.TriggerPx, "trigger price", id)
		if err != nil {
			return nil, err
		}
		var filled *float64
		if original != nil && remaining != nil {
			if *remaining > *original {
				return nil, fmt.Errorf("decode order %s: remaining amount exceeds original amount", id)
			}
			value := *original - *remaining
			filled = &value
		}
		result = append(result, OpenOrder{
			ID: id, Cloid: order.Cloid, Symbol: market.Symbol, Side: side,
			Type: orderType, OrderType: order.OrderType,
			Price: limitPrice, LimitPrice: limitPrice, TriggerPrice: triggerPrice,
			TriggerCondition: order.TriggerCondition, IsTrigger: order.IsTrigger, ReduceOnly: order.ReduceOnly,
			Amount: original, OriginalAmount: original, RemainingAmount: remaining,
			Filled: filled, Timestamp: order.Timestamp, Status: "open",
		})
	}
	return result, nil
}

func parseOrderNumber(value, field, id string) (*float64, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseFinite(value)
	if err != nil {
		return nil, fmt.Errorf("decode order %s %s: %w", id, field, err)
	}
	if parsed < 0 {
		return nil, fmt.Errorf("decode order %s %s: value must not be negative", id, field)
	}
	return &parsed, nil
}

func (c *Client) midpoint(ctx context.Context, market Market) (float64, error) {
	request := map[string]any{"type": "allMids"}
	if market.DEX != "" {
		request["dex"] = market.DEX
	}
	var mids map[string]string
	if err := c.info(ctx, request, &mids); err != nil {
		return 0, err
	}
	value, ok := mids[market.Coin]
	if !ok {
		return 0, fmt.Errorf("no midpoint returned for %s", market.Symbol)
	}
	mid, err := parseFinite(value)
	if err != nil || !finitePositive(mid) {
		return 0, fmt.Errorf("invalid midpoint for %s", market.Symbol)
	}
	return mid, nil
}

func positionMarkPrice(positionValue string, size float64) *float64 {
	value, err := parseFinite(positionValue)
	if err != nil || size == 0 {
		return nil
	}
	mark := math.Abs(value / size)
	if !finitePositive(mark) {
		return nil
	}
	return &mark
}

func parseOptionalString(value *string) *float64 {
	if value == nil {
		return nil
	}
	return parseOptionalValue(*value)
}

func parseOptionalValue(value string) *float64 {
	if value == "" {
		return nil
	}
	parsed, err := parseFinite(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseOptionalRaw(value json.RawMessage) *float64 {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	text := strings.Trim(string(value), "\"")
	return parseOptionalValue(text)
}

func parseOID(raw json.RawMessage) (string, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return "", errors.New("missing order id")
	}
	text = strings.Trim(text, "\"")
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(value, 10), nil
}
