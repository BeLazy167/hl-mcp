package hyperliquid

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const marketSlippage = 0.05

type PlaceOrderParams struct {
	Symbol                  string
	Side                    string
	Amount                  float64
	Price                   *float64
	StopLoss                *float64
	StopLossLimitPrice      *float64
	TakeProfit              *float64
	TakeProfitLimitPrice    *float64
	ReduceOnly              bool
	ParentClientOrderID     *string
	TakeProfitClientOrderID *string
	StopLossClientOrderID   *string
}

// MutationOptions binds one reserved nonce to optional expiry and durable pre-send checks.
type MutationOptions struct {
	Nonce        uint64
	ExpiresAfter *uint64
	// BeforeSend receives the unsigned action after signing succeeds. Any error blocks the network send.
	BeforeSend func(context.Context, json.RawMessage) error
	// BeforeNetworkSend runs immediately before HTTP send. Its release function runs after the send starts.
	BeforeNetworkSend func(context.Context) (release func(), err error)
}

type MutationResult struct {
	Nonce           uint64          `json:"nonce,omitempty"`
	Status          string          `json:"status"`
	ExchangeOrderID string          `json:"exchangeOrderId,omitempty"`
	Raw             json.RawMessage `json:"-"`
	Latency         time.Duration   `json:"-"`
	Order           *PlacedOrder    `json:"order,omitempty"`
	Attached        []OrderStatus   `json:"attachedOrders,omitempty"`
}

type PlacedOrder struct {
	Role   string   `json:"role"`
	Cloid  string   `json:"cloid"`
	ID     string   `json:"id,omitempty"`
	Symbol string   `json:"symbol"`
	Side   string   `json:"side"`
	Type   string   `json:"type"`
	Amount float64  `json:"amount"`
	Price  *float64 `json:"price,omitempty"`
	Status string   `json:"status"`
}

type OrderStatus struct {
	Role         string   `json:"role"`
	Cloid        string   `json:"cloid"`
	ID           string   `json:"id,omitempty"`
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
	TriggerPrice *float64 `json:"triggerPrice,omitempty"`
	LimitPrice   *float64 `json:"limitPrice,omitempty"`
}

type orderLeg struct {
	Role         string
	Cloid        string
	TriggerPrice *float64
	LimitPrice   *float64
}

type MutationError struct {
	Message   string
	Ambiguous bool
	Partial   bool
}

func (e *MutationError) Error() string { return e.Message }

type exchangePayload struct {
	Action       any       `json:"action"`
	Nonce        uint64    `json:"nonce"`
	Signature    Signature `json:"signature"`
	VaultAddress *string   `json:"vaultAddress"`
	ExpiresAfter *uint64   `json:"expiresAfter"`
}

type exchangeEnvelope struct {
	Status   *string         `json:"status"`
	Response json.RawMessage `json:"response"`
}

type exchangeData struct {
	Type string `json:"type"`
	Data struct {
		Statuses []json.RawMessage `json:"statuses"`
	} `json:"data"`
}

func (c *Client) PlaceOrder(ctx context.Context, params PlaceOrderParams) (MutationResult, error) {
	return c.PlaceOrderWithOptions(ctx, params, MutationOptions{Nonce: c.ReserveNonce()})
}

func (c *Client) PlaceOrderWithOptions(
	ctx context.Context, params PlaceOrderParams, options MutationOptions,
) (MutationResult, error) {
	if err := validatePlaceOrderCloids(params); err != nil {
		return MutationResult{Status: "failed"}, err
	}
	market, err := c.market(params.Symbol)
	if err != nil {
		return MutationResult{Status: "failed"}, err
	}
	side := strings.ToLower(strings.TrimSpace(params.Side))
	if side != "buy" && side != "sell" {
		return MutationResult{Status: "failed"}, errors.New("side must be buy or sell")
	}
	sizeWire, err := formatSize(params.Amount, market.SizeDecimal)
	if err != nil {
		return MutationResult{Status: "failed"}, err
	}

	isBuy := side == "buy"
	orderType := "limit"
	var parentPriceWire string
	var parentPrice float64
	if params.Price == nil {
		orderType = "market"
		midpoint, err := c.midpoint(ctx, market)
		if err != nil {
			return MutationResult{Status: "failed"}, fmt.Errorf("price market order: %w", err)
		}
		bound := midpoint * (1 - marketSlippage)
		if isBuy {
			bound = midpoint * (1 + marketSlippage)
		}
		parentPriceWire, parentPrice, err = formatPrice(bound, market.SizeDecimal)
	} else {
		parentPriceWire, parentPrice, err = formatPrice(*params.Price, market.SizeDecimal)
	}
	if err != nil {
		return MutationResult{Status: "failed"}, err
	}
	notional := parentPrice * params.Amount
	if !finitePositive(notional) {
		return MutationResult{Status: "failed"}, fmt.Errorf("could not determine notional for %s", market.Symbol)
	}
	if notional > c.maxNotional {
		if !params.ReduceOnly {
			return MutationResult{Status: "failed"}, fmt.Errorf(
				"order wire notional $%.2f exceeds MAX_NOTIONAL_USD $%.2f", notional, c.maxNotional,
			)
		}
		verified, verifyErr := c.verifiedReduceOnlyClose(ctx, market, isBuy, params.Amount)
		if verifyErr != nil {
			return MutationResult{Status: "failed"}, fmt.Errorf(
				"order wire notional $%.2f exceeds MAX_NOTIONAL_USD $%.2f; reduce-only close could not be verified: %w",
				notional, c.maxNotional, verifyErr,
			)
		}
		if !verified {
			return MutationResult{Status: "failed"}, fmt.Errorf(
				"order wire notional $%.2f exceeds MAX_NOTIONAL_USD $%.2f; reduce-only exemption requires a current opposite position at least as large as the order",
				notional, c.maxNotional,
			)
		}
	}

	parentType := OrderType{Limit: &LimitOrderType{TIF: "Gtc"}}
	if orderType == "market" {
		parentType.Limit.TIF = "Ioc"
	}
	parentCloid := cloidValue(params.ParentClientOrderID)
	orders := []OrderWire{{
		Asset: market.Asset, IsBuy: isBuy, Price: parentPriceWire, Size: sizeWire,
		ReduceOnly: params.ReduceOnly, Type: parentType, Cloid: parentCloid,
	}}
	legs := []orderLeg{{Role: "parent", Cloid: parentCloid}}
	grouping := "na"
	if params.TakeProfit != nil || params.StopLoss != nil {
		grouping = "normalTpsl"
	}
	appendTrigger := func(role, tpsl string, trigger float64, explicitLimit *float64, clientOrderID *string) error {
		triggerWire, triggerRounded, err := formatPrice(trigger, market.SizeDecimal)
		if err != nil {
			return fmt.Errorf("%s: %w", role, err)
		}
		exitIsBuy := !isBuy
		var limit float64
		if explicitLimit != nil {
			limit = *explicitLimit
		} else {
			limit = triggerRounded * (1 - marketSlippage)
			if exitIsBuy {
				limit = triggerRounded * (1 + marketSlippage)
			}
		}
		limitWire, limitRounded, err := formatPrice(limit, market.SizeDecimal)
		if err != nil {
			return fmt.Errorf("%s limit: %w", role, err)
		}
		if exitIsBuy && limitRounded < triggerRounded {
			return fmt.Errorf("%sLimitPrice must be at or above %s for a buy exit", role, role)
		}
		if !exitIsBuy && limitRounded > triggerRounded {
			return fmt.Errorf("%sLimitPrice must be at or below %s for a sell exit", role, role)
		}
		cloid := cloidValue(clientOrderID)
		orders = append(orders, OrderWire{
			Asset: market.Asset, IsBuy: exitIsBuy, Price: limitWire, Size: sizeWire, ReduceOnly: true,
			Type:  OrderType{Trigger: &TriggerOrderType{IsMarket: true, Trigger: triggerWire, TPSL: tpsl}},
			Cloid: cloid,
		})
		triggerPrice := triggerRounded
		limitPrice := limitRounded
		legs = append(legs, orderLeg{
			Role: role, Cloid: cloid, TriggerPrice: &triggerPrice, LimitPrice: &limitPrice,
		})
		return nil
	}
	if params.TakeProfit != nil {
		if err := appendTrigger(
			"takeProfit", "tp", *params.TakeProfit, params.TakeProfitLimitPrice, params.TakeProfitClientOrderID,
		); err != nil {
			return MutationResult{Status: "failed"}, err
		}
	}
	if params.StopLoss != nil {
		if err := appendTrigger(
			"stopLoss", "sl", *params.StopLoss, params.StopLossLimitPrice, params.StopLossClientOrderID,
		); err != nil {
			return MutationResult{Status: "failed"}, err
		}
	}

	action := OrderAction{Type: "order", Orders: orders, Grouping: grouping}
	mutation, err := c.submit(ctx, action, options)
	placed := &PlacedOrder{
		Role: "parent", Cloid: parentCloid, Symbol: market.Symbol, Side: side,
		Type: orderType, Amount: params.Amount, Price: &parentPrice, Status: mutation.Status,
	}
	mutation.Order = placed
	if len(mutation.Raw) == 0 {
		return mutation, err
	}

	statuses, statusErr := parseOrderStatuses(mutation.Raw, legs)
	if len(statuses) != 0 {
		parent := statuses[0]
		placed.ID = parent.ID
		placed.Status = parent.Status
		mutation.ExchangeOrderID = parent.ID
		mutation.Status = parent.Status
		if len(statuses) > 1 {
			mutation.Attached = statuses[1:]
		}
	}
	if err != nil {
		return mutation, err
	}
	if statusErr != nil {
		var typed *MutationError
		if errors.As(statusErr, &typed) && typed.Ambiguous {
			mutation.Status = "unknown"
			placed.Status = "unknown"
			return mutation, statusErr
		}
		acceptedParent := len(statuses) > 0 && statuses[0].Error == ""
		if acceptedParent {
			mutation.Status = "partial"
			return mutation, &MutationError{
				Message: "parent order may be live, but an attached TP/SL result failed: " + statusErr.Error(),
				Partial: true,
			}
		}
		mutation.Status = "failed"
		placed.Status = "failed"
		return mutation, statusErr
	}
	return mutation, nil
}

func validatePlaceOrderCloids(params PlaceOrderParams) error {
	if err := validateCloid("parentClientOrderId", params.ParentClientOrderID); err != nil {
		return err
	}
	if params.TakeProfit != nil && params.TakeProfitClientOrderID == nil {
		return errors.New("takeProfitClientOrderId is required when takeProfit is set")
	}
	if params.TakeProfit == nil && params.TakeProfitClientOrderID != nil {
		return errors.New("takeProfitClientOrderId requires takeProfit")
	}
	if params.TakeProfit == nil && params.TakeProfitLimitPrice != nil {
		return errors.New("takeProfitLimitPrice requires takeProfit")
	}
	if err := validateCloid("takeProfitClientOrderId", params.TakeProfitClientOrderID); err != nil {
		return err
	}
	if params.StopLoss != nil && params.StopLossClientOrderID == nil {
		return errors.New("stopLossClientOrderId is required when stopLoss is set")
	}
	if params.StopLoss == nil && params.StopLossClientOrderID != nil {
		return errors.New("stopLossClientOrderId requires stopLoss")
	}
	if params.StopLoss == nil && params.StopLossLimitPrice != nil {
		return errors.New("stopLossLimitPrice requires stopLoss")
	}
	return validateCloid("stopLossClientOrderId", params.StopLossClientOrderID)
}

func validateCloid(name string, value *string) error {
	if value == nil {
		return nil
	}
	if len(*value) != 34 || !strings.HasPrefix(*value, "0x") {
		return fmt.Errorf("%s must be exactly 0x followed by 32 hexadecimal characters", name)
	}
	if _, err := hex.DecodeString((*value)[2:]); err != nil {
		return fmt.Errorf("%s must be exactly 0x followed by 32 hexadecimal characters", name)
	}
	return nil
}

func cloidValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (c *Client) verifiedReduceOnlyClose(ctx context.Context, market Market, isBuy bool, amount float64) (bool, error) {
	state, err := c.userState(ctx, market.DEX)
	if err != nil {
		return false, err
	}
	for _, wrapper := range state.AssetPositions {
		position := wrapper.Position
		if position.Coin != market.Coin {
			continue
		}
		size, err := parseFinite(position.Size)
		if err != nil {
			return false, fmt.Errorf("decode %s position size: %w", market.Symbol, err)
		}
		if isBuy {
			return size < 0 && amount <= math.Abs(size), nil
		}
		return size > 0 && amount <= size, nil
	}
	return false, nil
}

func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int) (MutationResult, error) {
	return c.SetLeverageWithOptions(ctx, symbol, leverage, MutationOptions{Nonce: c.ReserveNonce()})
}

func (c *Client) SetLeverageWithOptions(
	ctx context.Context, symbol string, leverage int, options MutationOptions,
) (MutationResult, error) {
	market, err := c.market(symbol)
	if err != nil {
		return MutationResult{Status: "failed"}, err
	}
	if leverage < 1 || leverage > 50 || (market.MaxLeverage > 0 && leverage > market.MaxLeverage) {
		return MutationResult{Status: "failed"}, fmt.Errorf("leverage must be from 1 through %d for %s", market.MaxLeverage, market.Symbol)
	}
	result, err := c.submit(ctx, UpdateLeverageAction{
		Type: "updateLeverage", Asset: market.Asset, IsCross: true, Leverage: uint32(leverage),
	}, options)
	return checkDefaultMutationResponse(result, err)
}

func (c *Client) CancelOrder(ctx context.Context, id, symbol string) (MutationResult, error) {
	return c.CancelOrderWithOptions(ctx, id, symbol, MutationOptions{Nonce: c.ReserveNonce()})
}

func (c *Client) CancelOrderWithOptions(
	ctx context.Context, id, symbol string, options MutationOptions,
) (MutationResult, error) {
	market, err := c.market(symbol)
	if err != nil {
		return MutationResult{Status: "failed"}, err
	}
	oid, err := strconv.ParseUint(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return MutationResult{Status: "failed"}, errors.New("id must be an unsigned integer")
	}
	result, err := c.submit(ctx, CancelAction{
		Type: "cancel", Cancels: []CancelWire{{Asset: market.Asset, OID: oid}},
	}, options)
	result.ExchangeOrderID = strconv.FormatUint(oid, 10)
	return checkCancelStatuses(result, err, 1)
}

func (c *Client) CancelAll(ctx context.Context, symbol string) (MutationResult, error) {
	return c.CancelAllWithOptions(ctx, symbol, MutationOptions{Nonce: c.ReserveNonce()})
}

func (c *Client) CancelAllWithOptions(
	ctx context.Context, symbol string, options MutationOptions,
) (MutationResult, error) {
	market, err := c.market(symbol)
	if err != nil {
		return MutationResult{Status: "failed"}, err
	}
	orders, err := c.openOrdersForDEX(ctx, market.DEX)
	if err != nil {
		return MutationResult{Status: "failed"}, fmt.Errorf("list orders before cancellation: %w", err)
	}
	var cancels []CancelWire
	for _, order := range orders {
		if order.Coin != market.Coin {
			continue
		}
		id, err := parseOID(order.OID)
		if err != nil {
			return MutationResult{Status: "failed"}, err
		}
		oid, _ := strconv.ParseUint(id, 10, 64)
		cancels = append(cancels, CancelWire{Asset: market.Asset, OID: oid})
	}
	if len(cancels) == 0 {
		return MutationResult{
			Nonce: options.Nonce, Status: "succeeded",
			Raw: json.RawMessage(`{"status":"ok","response":{"type":"cancel","data":{"statuses":[]}}}`),
		}, nil
	}
	result, err := c.submit(ctx, CancelAction{Type: "cancel", Cancels: cancels}, options)
	return checkCancelStatuses(result, err, len(cancels))
}

func (c *Client) submit(ctx context.Context, action any, options MutationOptions) (MutationResult, error) {
	nonce := options.Nonce
	if nonce == 0 {
		return MutationResult{Status: "failed"}, errors.New("mutation nonce is required")
	}
	var expiresAfter *uint64
	if options.ExpiresAfter != nil {
		value := *options.ExpiresAfter
		expiresAfter = &value
	}
	if err := checkExpiresAfter(expiresAfter); err != nil {
		return MutationResult{Nonce: nonce, Status: "failed"}, err
	}
	signature, err := c.signer.SignActionWithExpiresAfter(
		action, nil, nonce, expiresAfter, c.mainnet,
	)
	if err != nil {
		return MutationResult{Nonce: nonce, Status: "failed"}, fmt.Errorf("sign action: %w", err)
	}
	if options.BeforeSend != nil {
		request, err := json.Marshal(action)
		if err != nil {
			return MutationResult{Nonce: nonce, Status: "failed"}, fmt.Errorf("encode venue request: %w", err)
		}
		if err := options.BeforeSend(ctx, request); err != nil {
			return MutationResult{Nonce: nonce, Status: "failed"}, fmt.Errorf("pre-send mutation check: %w", err)
		}
	}
	payload := exchangePayload{
		Action: action, Nonce: nonce, Signature: signature, VaultAddress: nil,
		ExpiresAfter: expiresAfter,
	}
	started := time.Now()
	raw, sent, err := c.postChecked(ctx, "/exchange", payload, func() (func(), error) {
		if err := checkExpiresAfter(expiresAfter); err != nil {
			return nil, err
		}
		if options.BeforeNetworkSend == nil {
			return nil, nil
		}
		return options.BeforeNetworkSend(ctx)
	})
	latency := time.Since(started)
	result := MutationResult{Nonce: nonce, Status: "unknown", Raw: raw, Latency: latency}
	if err != nil && !sent {
		result.Status = "failed"
		return result, fmt.Errorf("exchange request blocked before send: %w", err)
	}
	if err != nil {
		return result, &MutationError{
			Message: "exchange outcome unknown; do not retry blindly: " + err.Error(), Ambiguous: true,
		}
	}
	var envelope exchangeEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return result, &MutationError{
			Message: "exchange outcome unknown; malformed response: " + err.Error(), Ambiguous: true,
		}
	}
	if envelope.Status == nil || *envelope.Status == "" {
		return result, &MutationError{
			Message: "exchange outcome unknown; malformed response: missing status", Ambiguous: true,
		}
	}
	if *envelope.Status != "ok" {
		message := strings.TrimSpace(string(envelope.Response))
		message = strings.Trim(message, "\"")
		if *envelope.Status == "err" {
			result.Status = "failed"
			return result, errors.New("Hyperliquid rejected action: " + message)
		}
		return result, &MutationError{
			Message:   "exchange outcome unknown; unexpected status " + *envelope.Status + ": " + message,
			Ambiguous: true,
		}
	}
	result.Status = "succeeded"
	return result, nil
}

func checkExpiresAfter(expiresAfter *uint64) error {
	if expiresAfter == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	if *expiresAfter == 0 || (now >= 0 && uint64(now) >= *expiresAfter) {
		return errors.New("mutation fence expired before network send")
	}
	return nil
}

func checkCancelStatuses(result MutationResult, err error, expected int) (MutationResult, error) {
	if err != nil || len(result.Raw) == 0 {
		return result, err
	}
	var envelope exchangeEnvelope
	if err := json.Unmarshal(result.Raw, &envelope); err != nil {
		return ambiguousCancelResult(result, "malformed exchange envelope: "+err.Error())
	}
	if envelope.Status == nil || *envelope.Status != "ok" {
		return ambiguousCancelResult(result, "unexpected or missing exchange envelope status")
	}
	var response exchangeData
	if err := json.Unmarshal(envelope.Response, &response); err != nil {
		return ambiguousCancelResult(result, "malformed cancel response: "+err.Error())
	}
	if response.Type != "cancel" {
		return ambiguousCancelResult(result, "unexpected cancel response type "+strconv.Quote(response.Type))
	}
	if len(response.Data.Statuses) != expected {
		return ambiguousCancelResult(result, fmt.Sprintf(
			"hyperliquid returned %d cancel statuses for %d cancellations",
			len(response.Data.Statuses), expected,
		))
	}

	succeeded := 0
	failures := make([]string, 0, expected)
	for _, rawStatus := range response.Data.Statuses {
		var text string
		if json.Unmarshal(rawStatus, &text) == nil {
			if text != "success" {
				return ambiguousCancelResult(result, "unexpected cancel status "+strconv.Quote(text))
			}
			succeeded++
			continue
		}

		var object map[string]json.RawMessage
		if err := json.Unmarshal(rawStatus, &object); err != nil || object == nil || len(object) != 1 {
			return ambiguousCancelResult(result, "malformed cancel status")
		}
		rawError, ok := object["error"]
		if !ok {
			return ambiguousCancelResult(result, "unknown cancel status object")
		}
		var message string
		if err := json.Unmarshal(rawError, &message); err != nil || strings.TrimSpace(message) == "" {
			return ambiguousCancelResult(result, "malformed cancel error")
		}
		failures = append(failures, message)
	}
	if len(failures) == 0 {
		return result, nil
	}
	message := strings.Join(failures, "; ")
	if succeeded > 0 {
		result.Status = "partial"
		return result, &MutationError{
			Message: "some cancellations succeeded and some failed: " + message,
			Partial: true,
		}
	}
	result.Status = "failed"
	return result, errors.New(message)
}

func ambiguousCancelResult(result MutationResult, message string) (MutationResult, error) {
	result.Status = "unknown"
	return result, &MutationError{
		Message:   "cancel outcome unknown; do not retry blindly: " + message,
		Ambiguous: true,
	}
}

func checkDefaultMutationResponse(result MutationResult, err error) (MutationResult, error) {
	if err != nil || len(result.Raw) == 0 {
		return result, err
	}
	var envelope exchangeEnvelope
	if err := json.Unmarshal(result.Raw, &envelope); err != nil {
		return ambiguousMutationResult(result, "malformed exchange envelope: "+err.Error())
	}
	if envelope.Status == nil || *envelope.Status != "ok" {
		return ambiguousMutationResult(result, "unexpected or missing exchange envelope status")
	}
	var response exchangeData
	if err := json.Unmarshal(envelope.Response, &response); err != nil {
		return ambiguousMutationResult(result, "malformed default response: "+err.Error())
	}
	if response.Type != "default" {
		return ambiguousMutationResult(result, "unexpected default response type "+strconv.Quote(response.Type))
	}
	if len(response.Data.Statuses) != 0 {
		return ambiguousMutationResult(result, "default response unexpectedly contained action statuses")
	}
	return result, nil
}

func ambiguousMutationResult(result MutationResult, message string) (MutationResult, error) {
	result.Status = "unknown"
	return result, &MutationError{
		Message:   "mutation outcome unknown; do not retry blindly: " + message,
		Ambiguous: true,
	}
}

func parseOrderStatuses(raw json.RawMessage, legs []orderLeg) ([]OrderStatus, error) {
	var envelope exchangeEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, ambiguousOrderResponse("malformed exchange envelope: " + err.Error())
	}
	if envelope.Status == nil || *envelope.Status == "" {
		return nil, ambiguousOrderResponse("malformed exchange envelope: missing status")
	}
	if *envelope.Status != "ok" {
		return nil, ambiguousOrderResponse("unexpected exchange envelope status " + strconv.Quote(*envelope.Status))
	}
	var response exchangeData
	if err := json.Unmarshal(envelope.Response, &response); err != nil {
		return nil, ambiguousOrderResponse("malformed order response: " + err.Error())
	}
	if response.Type != "order" {
		return nil, ambiguousOrderResponse("unexpected order response type " + strconv.Quote(response.Type))
	}

	result := make([]OrderStatus, 0, len(response.Data.Statuses))
	var failures []string
	ambiguous := len(response.Data.Statuses) != len(legs)
	if ambiguous {
		failures = append(failures, fmt.Sprintf(
			"hyperliquid returned %d order statuses for %d orders", len(response.Data.Statuses), len(legs),
		))
	}
	for index, rawStatus := range response.Data.Statuses {
		leg := orderLeg{Role: "order"}
		if index < len(legs) {
			leg = legs[index]
		}
		status := OrderStatus{
			Role: leg.Role, Cloid: leg.Cloid, Status: "open",
			TriggerPrice: leg.TriggerPrice, LimitPrice: leg.LimitPrice,
		}
		var text string
		if json.Unmarshal(rawStatus, &text) == nil {
			switch text {
			case "waitingForTrigger", "waitingForFill":
				status.Status = text
			case "success":
				status.Status = "open"
			case "":
				status.Status = "unknown"
				status.Error = "empty order status"
				failures = append(failures, leg.Role+": empty order status")
				ambiguous = true
			default:
				status.Status = "failed"
				status.Error = text
				failures = append(failures, leg.Role+": "+text)
			}
			result = append(result, status)
			continue
		}

		var object map[string]json.RawMessage
		if err := json.Unmarshal(rawStatus, &object); err != nil || object == nil {
			status.Status = "unknown"
			status.Error = "malformed order status"
			failures = append(failures, leg.Role+": malformed order status")
			ambiguous = true
			result = append(result, status)
			continue
		}
		_, hasError := object["error"]
		_, hasResting := object["resting"]
		_, hasFilled := object["filled"]
		variants := 0
		for _, present := range []bool{hasError, hasResting, hasFilled} {
			if present {
				variants++
			}
		}
		if variants != 1 {
			status.Status = "unknown"
			status.Error = "unknown or conflicting order status"
			failures = append(failures, leg.Role+": "+status.Error)
			ambiguous = true
			result = append(result, status)
			continue
		}

		switch {
		case hasError:
			if err := json.Unmarshal(object["error"], &status.Error); err != nil || status.Error == "" {
				status.Status = "unknown"
				status.Error = "malformed order error"
				ambiguous = true
			} else {
				status.Status = "failed"
			}
			failures = append(failures, leg.Role+": "+status.Error)
		case hasResting:
			var resting struct {
				OID json.RawMessage `json:"oid"`
			}
			if err := json.Unmarshal(object["resting"], &resting); err != nil {
				status.Status = "unknown"
				status.Error = "malformed resting order status"
				failures = append(failures, leg.Role+": "+status.Error)
				ambiguous = true
				break
			}
			var err error
			status.ID, err = parseOID(resting.OID)
			status.Status = "open"
			if err != nil {
				status.Status = "unknown"
				status.Error = "invalid resting order id"
				failures = append(failures, leg.Role+": "+status.Error)
				ambiguous = true
			}
		case hasFilled:
			var filled struct {
				OID json.RawMessage `json:"oid"`
			}
			if err := json.Unmarshal(object["filled"], &filled); err != nil {
				status.Status = "unknown"
				status.Error = "malformed filled order status"
				failures = append(failures, leg.Role+": "+status.Error)
				ambiguous = true
				break
			}
			var err error
			status.ID, err = parseOID(filled.OID)
			status.Status = "closed"
			if err != nil {
				status.Status = "unknown"
				status.Error = "invalid filled order id"
				failures = append(failures, leg.Role+": "+status.Error)
				ambiguous = true
			}
		}
		result = append(result, status)
	}
	if len(failures) == 0 {
		return result, nil
	}
	message := strings.Join(failures, "; ")
	if ambiguous {
		return result, ambiguousOrderResponse(message)
	}
	return result, errors.New(message)
}

func ambiguousOrderResponse(message string) error {
	return &MutationError{
		Message:   "order outcome unknown; do not retry blindly: " + message,
		Ambiguous: true,
	}
}
