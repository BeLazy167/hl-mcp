package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHyperliquid struct {
	server *httptest.Server

	mu              sync.Mutex
	infoRequests    []map[string]any
	exchangeBodies  []map[string]any
	exchangeActions []map[string]any
	exchangeNonces  []uint64
	exchangeReply   any
	authorizedUser  string
}

func newFakeHyperliquid(t *testing.T) *fakeHyperliquid {
	t.Helper()
	fake := &fakeHyperliquid{}
	fake.exchangeReply = map[string]any{
		"status":   "ok",
		"response": map[string]any{"type": "default"},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		switch request.URL.Path {
		case "/info":
			fake.mu.Lock()
			fake.infoRequests = append(fake.infoRequests, body)
			fake.mu.Unlock()
			fake.writeInfo(t, response, body)
		case "/exchange":
			action, _ := body["action"].(map[string]any)
			nonce, _ := body["nonce"].(float64)
			fake.mu.Lock()
			fake.exchangeBodies = append(fake.exchangeBodies, body)
			fake.exchangeActions = append(fake.exchangeActions, action)
			fake.exchangeNonces = append(fake.exchangeNonces, uint64(nonce))
			reply := fake.exchangeReply
			fake.mu.Unlock()
			_ = json.NewEncoder(response).Encode(reply)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeHyperliquid) writeInfo(t *testing.T, response http.ResponseWriter, request map[string]any) {
	t.Helper()
	typeName, _ := request["type"].(string)
	dex, _ := request["dex"].(string)
	switch typeName {
	case "perpDexs":
		_, _ = response.Write([]byte(`[null,{"name":"xyz"}]`))
	case "spotMeta":
		_, _ = response.Write([]byte(`{"tokens":[{"name":"USDC","index":0}]}`))
	case "metaAndAssetCtxs":
		if dex == "xyz" {
			_, _ = response.Write([]byte(`[{"collateralToken":0,"universe":[{"name":"xyz:GOLD","szDecimals":2,"maxLeverage":25}]},[{"midPx":"100","markPx":"100","impactPxs":["99","101"]}]]`))
		} else {
			_, _ = response.Write([]byte(`[{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":40}]},[{"midPx":"50000","markPx":"50000","impactPxs":["49999","50001"]}]]`))
		}
	case "userAbstraction":
		_, _ = response.Write([]byte(`"default"`))
	case "userRole":
		f.mu.Lock()
		authorizedUser := f.authorizedUser
		f.mu.Unlock()
		if authorizedUser == "" {
			_, _ = response.Write([]byte(`{"role":"user"}`))
		} else {
			_ = json.NewEncoder(response).Encode(map[string]any{
				"role": "agent", "data": map[string]any{"user": authorizedUser},
			})
		}
	case "clearinghouseState":
		if dex == "xyz" {
			_, _ = response.Write([]byte(`{"assetPositions":[{"position":{"coin":"xyz:GOLD","entryPx":"90","liquidationPx":"50","positionValue":"100","szi":"1","unrealizedPnl":"10","leverage":{"value":5}}}],"marginSummary":{"accountValue":"200","totalMarginUsed":"20"}}`))
		} else {
			_, _ = response.Write([]byte(`{"assetPositions":[],"marginSummary":{"accountValue":"200","totalMarginUsed":"20"}}`))
		}
	case "frontendOpenOrders":
		if dex == "xyz" {
			_, _ = response.Write([]byte(`[{"coin":"xyz:GOLD","cloid":"0x77777777777777777777777777777777","isTrigger":true,"limitPx":"95","triggerPx":"96","triggerCondition":"Price below 96","oid":77,"orderType":"Stop Market","origSz":"1.5","reduceOnly":true,"side":"A","sz":"1","timestamp":1700000000123}]`))
		} else {
			_, _ = response.Write([]byte(`[]`))
		}
	case "allMids":
		if dex == "xyz" {
			_, _ = response.Write([]byte(`{"xyz:GOLD":"100"}`))
		} else {
			_, _ = response.Write([]byte(`{"BTC":"50000"}`))
		}
	case "l2Book":
		_, _ = response.Write([]byte(`{"coin":"xyz:GOLD","time":123,"levels":[[{"px":"99","sz":"2","n":3}],[{"px":"101","sz":"4","n":5}]]}`))
	case "candleSnapshot":
		_, _ = response.Write([]byte(`[{"t":1000,"T":1999,"s":"xyz:GOLD","i":"1m","o":"99","c":"100","h":"101","l":"98","v":"12.5","n":7}]`))
	case "userFills", "userFillsByTime", "historicalOrders", "fundingHistory", "userFunding":
		_, _ = response.Write([]byte(`[{"time":2000},{"time":1000}]`))
	case "orderStatus":
		_, _ = response.Write([]byte(`{"status":"unknownOid"}`))
	case "predictedFundings":
		_, _ = response.Write([]byte(`[["BTC",[["HlPerp",{"fundingRate":"0.0001","nextFundingTime":2000}]]]]`))
	case "portfolio":
		_, _ = response.Write([]byte(`[["day",{"accountValueHistory":[],"pnlHistory":[],"vlm":"0"}]]`))
	case "userFees":
		_, _ = response.Write([]byte(`{"dailyUserVlm":[],"feeSchedule":{"cross":"0.00045"}}`))
	case "userRateLimit":
		_, _ = response.Write([]byte(`{"cumVlm":"0","nRequestsUsed":1,"nRequestsCap":10000}`))
	case "spotClearinghouseState":
		_, _ = response.Write([]byte(`{"balances":[{"coin":"USDC","hold":"5","total":"100"},{"coin":"HYPE","hold":"0","total":"2"}]}`))
	case "activeAssetData":
		_, _ = response.Write([]byte(`{"user":"0x1111111111111111111111111111111111111111","coin":"xyz:GOLD","leverage":{"type":"cross","value":5},"maxTradeSzs":["10","11"],"availableToTrade":["8","9"],"markPx":"100"}`))
	default:
		t.Errorf("unexpected info request: %v", request)
		response.WriteHeader(http.StatusBadRequest)
	}
}

func newTestClient(t *testing.T, fake *fakeHyperliquid) *Client {
	t.Helper()
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Options{
		BaseURL: fake.server.URL, WalletAddress: signer.Address(), PrivateKey: vectorPrivateKey,
		DEXes: []string{"xyz"}, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAccountIdentityIsNormalizedAndPublic(t *testing.T) {
	for _, test := range []struct {
		name        string
		wallet      string
		wantMainnet bool
	}{
		{name: "mainnet address without prefix", wallet: "ABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCD", wantMainnet: true},
		{name: "testnet uppercase address", wallet: "0xABCDEFABCDEFABCDEFABCDEFABCDEFABCDEFABCD", wantMainnet: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			fake.authorizedUser = "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"
			client, err := NewClient(Options{
				BaseURL: fake.server.URL, WalletAddress: test.wallet, PrivateKey: vectorPrivateKey,
				MaxNotional: 1_000_000, Timeout: 2 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			client.mainnet = test.wantMainnet
			t.Cleanup(client.Close)

			identity, err := client.AccountIdentity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if identity.Address != "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" ||
				identity.Mainnet != test.wantMainnet || !identity.AssociationVerified ||
				identity.AuthorizedAccountAddress != identity.Address {
				t.Fatalf("identity = %+v", identity)
			}
			encoded, err := json.Marshal(identity)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), vectorPrivateKey) || !strings.Contains(string(encoded), `"associationVerified":true`) {
				t.Fatalf("encoded identity = %s", encoded)
			}
		})
	}
}

func TestAccountIdentityRejectsSignerForDifferentFundedAccount(t *testing.T) {
	fake := newFakeHyperliquid(t)
	fake.authorizedUser = "0x2222222222222222222222222222222222222222"
	client, err := NewClient(Options{
		BaseURL:       fake.server.URL,
		WalletAddress: "0x1111111111111111111111111111111111111111",
		PrivateKey:    vectorPrivateKey,
		MaxNotional:   1_000_000,
		Timeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	identity, err := client.AccountIdentity(context.Background())
	if err == nil || identity.AssociationVerified {
		t.Fatalf("identity = %+v, err = %v; want rejected association", identity, err)
	}
}

func TestSubmitRecordsExactActionAndNonceBeforeNetworkSend(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client, err := NewClient(Options{
		BaseURL: fake.server.URL, WalletAddress: "0x1111111111111111111111111111111111111111",
		PrivateKey: vectorPrivateKey, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	nonce := uint64(1700000000789)
	action := CancelAction{Type: "cancel", Cancels: []CancelWire{{Asset: 1, OID: 42}}}
	called := false
	result, err := client.submit(context.Background(), action, MutationOptions{
		Nonce: nonce,
		BeforeSend: func(_ context.Context, request json.RawMessage) error {
			called = true
			if string(request) != `{"type":"cancel","cancels":[{"a":1,"o":42}]}` {
				return fmt.Errorf("venue request = %s", request)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.exchangeBodies) != 0 {
				return errors.New("exchange send happened before durable callback")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.Nonce != nonce {
		t.Fatalf("called = %t, result = %+v", called, result)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeNonces) != 1 || fake.exchangeNonces[0] != nonce {
		t.Fatalf("exchange nonces = %+v", fake.exchangeNonces)
	}
}

func TestSubmitSignsAndSendsExpiresAfter(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client, err := NewClient(Options{
		BaseURL: fake.server.URL, WalletAddress: "0x1111111111111111111111111111111111111111",
		PrivateKey: vectorPrivateKey, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	nonce := uint64(1700000000789)
	expiresAfter := uint64(time.Now().Add(time.Minute).UnixMilli())
	action := UpdateLeverageAction{Type: "updateLeverage", Asset: 1, IsCross: true, Leverage: 5}
	if _, err := client.submit(context.Background(), action, MutationOptions{
		Nonce: nonce, ExpiresAfter: &expiresAfter,
	}); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeBodies) != 1 {
		t.Fatalf("exchange requests = %d", len(fake.exchangeBodies))
	}
	payload := fake.exchangeBodies[0]
	if payload["expiresAfter"] != float64(expiresAfter) {
		t.Fatalf("expiresAfter = %#v, want %d", payload["expiresAfter"], expiresAfter)
	}
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	want, err := signer.SignActionWithExpiresAfter(action, nil, nonce, &expiresAfter, true)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := signer.SignAction(action, nil, nonce, true)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := payload["signature"].(map[string]any)
	if !ok || got["r"] != want.R || got["s"] != want.S || got["v"] != float64(want.V) {
		t.Fatalf("signature = %#v, want %+v", payload["signature"], want)
	}
	if want == legacy {
		t.Fatal("expiresAfter did not affect the signed action")
	}
}

func TestSubmitRechecksExpiryImmediatelyBeforeNetworkSend(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client, err := NewClient(Options{
		BaseURL: fake.server.URL, WalletAddress: "0x1111111111111111111111111111111111111111",
		PrivateKey: vectorPrivateKey, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	expiresAfter := uint64(time.Now().Add(100 * time.Millisecond).UnixMilli())
	action := UpdateLeverageAction{Type: "updateLeverage", Asset: 1, IsCross: true, Leverage: 5}
	result, err := client.submit(context.Background(), action, MutationOptions{
		Nonce: 1700000000790, ExpiresAfter: &expiresAfter,
		BeforeSend: func(ctx context.Context, _ json.RawMessage) error {
			delay := time.Until(time.UnixMilli(int64(expiresAfter))) + 10*time.Millisecond
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "expired before network send") || result.Status != "failed" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeBodies) != 0 {
		t.Fatalf("exchange requests = %d, want 0", len(fake.exchangeBodies))
	}
}

func TestSubmitDoesNotSendWhenDurableCallbackFails(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client, err := NewClient(Options{
		BaseURL: fake.server.URL, WalletAddress: "0x1111111111111111111111111111111111111111",
		PrivateKey: vectorPrivateKey, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	failure := errors.New("audit disk unavailable")
	result, err := client.submit(context.Background(), CancelAction{
		Type: "cancel", Cancels: []CancelWire{{Asset: 1, OID: 42}},
	}, MutationOptions{
		Nonce: 1700000000790,
		BeforeSend: func(context.Context, json.RawMessage) error {
			return failure
		},
	})
	if !errors.Is(err, failure) || result.Status != "failed" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeBodies) != 0 {
		t.Fatalf("exchange requests = %d", len(fake.exchangeBodies))
	}
}

func TestAgentWalletLeavesVaultAddressNull(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client, err := NewClient(Options{
		BaseURL: fake.server.URL, WalletAddress: "0x1111111111111111111111111111111111111111",
		PrivateKey: vectorPrivateKey, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	action := CancelAction{Type: "cancel", Cancels: []CancelWire{{Asset: 1, OID: 42}}}
	if _, err := client.submit(context.Background(), action, MutationOptions{Nonce: client.ReserveNonce()}); err != nil {
		t.Fatal(err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeBodies) != 1 {
		t.Fatalf("exchange requests = %d", len(fake.exchangeBodies))
	}
	payload := fake.exchangeBodies[0]
	vault, present := payload["vaultAddress"]
	if !present || vault != nil {
		t.Fatalf("vaultAddress = %#v, present = %t; want explicit null", vault, present)
	}
	nonce, ok := payload["nonce"].(float64)
	if !ok {
		t.Fatalf("nonce = %#v", payload["nonce"])
	}
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	want, err := signer.SignAction(action, nil, uint64(nonce), true)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := payload["signature"].(map[string]any)
	if !ok || got["r"] != want.R || got["s"] != want.S || got["v"] != float64(want.V) {
		t.Fatalf("signature did not use a nil vault: %#v", payload["signature"])
	}
}

func TestExpandedReadRequests(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)

	nSigFigs, mantissa := 5, 2
	book, err := client.OrderBook(context.Background(), OrderBookParams{
		Symbol: "XYZ-GOLD/USDC:USDC", NSigFigs: &nSigFigs, Mantissa: &mantissa,
	})
	if err != nil {
		t.Fatal(err)
	}
	if book.Symbol != "XYZ-GOLD/USDC:USDC" || len(book.Bids) != 1 || len(book.Asks) != 1 ||
		book.Bids[0].Price != 99 || book.Asks[0].Orders != 5 {
		t.Fatalf("book = %+v", book)
	}

	candles, err := client.Candles(context.Background(), CandlesParams{
		Symbol: "XYZ-GOLD/USDC:USDC", Interval: "1m", StartTime: 1000, EndTime: 2000, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Symbol != "XYZ-GOLD/USDC:USDC" || candles[0].Close != 100 {
		t.Fatalf("candles = %+v", candles)
	}

	startTime, endTime := int64(1000), int64(2000)
	if _, err := client.UserFills(context.Background(), UserFillsParams{
		StartTime: &startTime, EndTime: &endTime, AggregateByTime: true, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := client.OrderHistory(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("bounded order history length = %d", len(history))
	}
	if _, err := client.OrderStatus(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FundingHistory(context.Background(), FundingHistoryParams{
		Symbol: "XYZ-GOLD/USDC:USDC", StartTime: startTime, EndTime: &endTime, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UserFunding(context.Background(), UserFundingParams{
		StartTime: startTime, EndTime: &endTime, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
	btc := "BTC/USDC:USDC"
	predicted, err := client.PredictedFunding(context.Background(), &btc)
	if err != nil {
		t.Fatal(err)
	}
	if len(predicted) != 1 {
		t.Fatalf("predicted funding = %s", predicted)
	}
	if _, err := client.Portfolio(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Fees(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RateLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	balances, err := client.SpotBalances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(balances) != 2 || balances[0].Coin != "USDC" || balances[0].Free != 95 {
		t.Fatalf("spot balances = %+v", balances)
	}
	active, err := client.ActiveAssetData(context.Background(), "XYZ-GOLD/USDC:USDC")
	if err != nil {
		t.Fatal(err)
	}
	if active.Symbol != "XYZ-GOLD/USDC:USDC" || active.MarkPrice != 100 ||
		len(active.MaxTradeSizes) != 2 || active.MaxTradeSizes[0] != 10 {
		t.Fatalf("active asset data = %+v", active)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	byType := make(map[string]map[string]any)
	for _, request := range fake.infoRequests {
		typeName, _ := request["type"].(string)
		byType[typeName] = request
	}
	bookRequest := byType["l2Book"]
	if bookRequest["coin"] != "xyz:GOLD" || bookRequest["nSigFigs"] != float64(5) || bookRequest["mantissa"] != float64(2) {
		t.Fatalf("l2Book request = %#v", bookRequest)
	}
	candleRequest := byType["candleSnapshot"]
	candleBody, ok := candleRequest["req"].(map[string]any)
	if !ok || candleBody["coin"] != "xyz:GOLD" || candleBody["interval"] != "1m" {
		t.Fatalf("candle request = %#v", candleRequest)
	}
	fillsRequest := byType["userFillsByTime"]
	if fillsRequest["user"] != client.walletAddress || fillsRequest["startTime"] != float64(1000) || fillsRequest["aggregateByTime"] != true {
		t.Fatalf("fills request = %#v", fillsRequest)
	}
	statusRequest := byType["orderStatus"]
	if statusRequest["user"] != client.walletAddress || statusRequest["oid"] != float64(123) {
		t.Fatalf("order status request = %#v", statusRequest)
	}
	fundingRequest := byType["fundingHistory"]
	if fundingRequest["coin"] != "xyz:GOLD" || fundingRequest["startTime"] != float64(1000) {
		t.Fatalf("funding request = %#v", fundingRequest)
	}
	activeRequest := byType["activeAssetData"]
	if activeRequest["coin"] != "xyz:GOLD" || activeRequest["user"] != client.walletAddress {
		t.Fatalf("active asset request = %#v", activeRequest)
	}
}

func TestExpandedReadValidationStopsBeforeNetwork(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)
	fake.mu.Lock()
	requestCount := len(fake.infoRequests)
	fake.mu.Unlock()

	nSigFigs, mantissa := 4, 1
	invalidCalls := []func() error{
		func() error {
			_, err := client.OrderBook(context.Background(), OrderBookParams{
				Symbol: "BTC/USDC:USDC", NSigFigs: &nSigFigs, Mantissa: &mantissa,
			})
			return err
		},
		func() error {
			_, err := client.Candles(context.Background(), CandlesParams{
				Symbol: "BTC/USDC:USDC", Interval: "10m", StartTime: 0, EndTime: 1, Limit: 10,
			})
			return err
		},
		func() error {
			endTime := int64(1)
			_, err := client.UserFills(context.Background(), UserFillsParams{EndTime: &endTime, Limit: 10})
			return err
		},
		func() error {
			endTime := int64(1)
			_, err := client.FundingHistory(context.Background(), FundingHistoryParams{
				Symbol: "BTC/USDC:USDC", StartTime: 2, EndTime: &endTime, Limit: 10,
			})
			return err
		},
		func() error {
			_, err := client.OrderHistory(context.Background(), 0)
			return err
		},
		func() error {
			_, err := client.OrderStatus(context.Background(), "not-an-order")
			return err
		},
		func() error {
			symbol := "XYZ-GOLD/USDC:USDC"
			_, err := client.PredictedFunding(context.Background(), &symbol)
			return err
		},
	}
	for index, call := range invalidCalls {
		if err := call(); err == nil {
			t.Fatalf("invalid call %d succeeded", index)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.infoRequests) != requestCount {
		t.Fatalf("invalid calls sent %d info requests", len(fake.infoRequests)-requestCount)
	}
}

func TestPerDEXPositionsAndOpenOrders(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)

	markets, err := client.SearchMarkets("GOLD", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(markets) != 1 || markets[0] != (MarketSummary{
		Symbol: "XYZ-GOLD/USDC:USDC", Coin: "xyz:GOLD", DEX: "xyz", AssetID: 110000,
		SizeDecimals: 2, MaxLeverage: 25, MinCostUSD: 10,
	}) {
		t.Fatalf("markets = %+v", markets)
	}

	positions, err := client.Positions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].Symbol != "XYZ-GOLD/USDC:USDC" ||
		positions[0].MarkPrice == nil || *positions[0].MarkPrice != 100 {
		t.Fatalf("positions = %+v", positions)
	}
	orders, err := client.OpenOrders(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %+v", orders)
	}
	order := orders[0]
	if order.ID != "77" || order.Cloid == nil || *order.Cloid != "0x77777777777777777777777777777777" ||
		order.Type != "market" || order.OrderType != "Stop Market" || order.Price == nil || *order.Price != 95 ||
		order.LimitPrice == nil || *order.LimitPrice != 95 || order.TriggerPrice == nil || *order.TriggerPrice != 96 ||
		order.TriggerCondition != "Price below 96" || order.IsTrigger == nil || !*order.IsTrigger ||
		order.ReduceOnly == nil || !*order.ReduceOnly || order.Amount == nil || *order.Amount != 1.5 ||
		order.OriginalAmount == nil || *order.OriginalAmount != 1.5 ||
		order.RemainingAmount == nil || *order.RemainingAmount != 1 || order.Filled == nil || *order.Filled != 0.5 ||
		order.Timestamp == nil || *order.Timestamp != 1700000000123 {
		t.Fatalf("order = %+v", order)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	var mainPositions, xyzPositions, mainOrders, xyzOrders int
	for _, request := range fake.infoRequests {
		typeName, _ := request["type"].(string)
		dex, _ := request["dex"].(string)
		switch typeName + ":" + dex {
		case "clearinghouseState:":
			mainPositions++
		case "clearinghouseState:xyz":
			xyzPositions++
		case "frontendOpenOrders:":
			mainOrders++
		case "frontendOpenOrders:xyz":
			xyzOrders++
		}
	}
	if mainPositions != 1 || xyzPositions != 1 || mainOrders != 1 || xyzOrders != 1 {
		t.Fatalf("per-DEX reads = main positions %d, xyz positions %d, main orders %d, xyz orders %d", mainPositions, xyzPositions, mainOrders, xyzOrders)
	}
}

func TestSymbolOrderReadTargetsDEXWithoutServerSideSymbolFilter(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)
	symbol := "XYZ-GOLD/USDC:USDC"
	orders, err := client.OpenOrders(context.Background(), &symbol)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders = %+v", orders)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	last := fake.infoRequests[len(fake.infoRequests)-1]
	if last["type"] != "frontendOpenOrders" || last["dex"] != "xyz" {
		t.Fatalf("request = %#v", last)
	}
	if _, hasCoin := last["coin"]; hasCoin {
		t.Fatalf("server-side coin filter would hide sibling orders: %#v", last)
	}
}

func TestOpenOrdersRejectMalformedEnrichedFields(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)
	tests := []struct {
		name  string
		order frontendOrder
	}{
		{
			name: "unknown side",
			order: frontendOrder{
				Coin: "xyz:GOLD", OID: json.RawMessage(`1`), Side: "X",
				OrderType: "Limit", LimitPx: "100", OrigSize: "1", Size: "1",
			},
		},
		{
			name: "malformed limit price",
			order: frontendOrder{
				Coin: "xyz:GOLD", OID: json.RawMessage(`1`), Side: "B",
				OrderType: "Limit", LimitPx: "bad", OrigSize: "1", Size: "1",
			},
		},
		{
			name: "remaining exceeds original",
			order: frontendOrder{
				Coin: "xyz:GOLD", OID: json.RawMessage(`1`), Side: "B",
				OrderType: "Limit", LimitPx: "100", OrigSize: "1", Size: "2",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.parseOpenOrders([]frontendOrder{test.order}, nil); err == nil {
				t.Fatal("expected malformed open-order error")
			}
		})
	}
}

func TestMarketOrderPricingAndAttachedTPSL(t *testing.T) {
	fake := newFakeHyperliquid(t)
	fake.exchangeReply = map[string]any{
		"status": "ok",
		"response": map[string]any{
			"type": "order",
			"data": map[string]any{"statuses": []any{
				map[string]any{"resting": map[string]any{"oid": 123}},
				"waitingForFill", "waitingForTrigger",
			}},
		},
	}
	client := newTestClient(t, fake)
	stop, take := 90.0, 110.0
	stopLimit, takeLimit := 89.876, 109.876
	parentCloid := "0x11111111111111111111111111111111"
	takeCloid := "0x22222222222222222222222222222222"
	stopCloid := "0x33333333333333333333333333333333"
	result, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1,
		StopLoss: &stop, StopLossLimitPrice: &stopLimit,
		TakeProfit: &take, TakeProfitLimitPrice: &takeLimit,
		ParentClientOrderID: &parentCloid, TakeProfitClientOrderID: &takeCloid,
		StopLossClientOrderID: &stopCloid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Order == nil || result.Order.Role != "parent" || result.Order.Cloid != parentCloid ||
		result.Order.ID != "123" || result.Order.Type != "market" || len(result.Attached) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Attached[0].Role != "takeProfit" || result.Attached[0].Cloid != takeCloid ||
		result.Attached[0].Status != "waitingForFill" || result.Attached[0].TriggerPrice == nil ||
		*result.Attached[0].TriggerPrice != 110 || result.Attached[0].LimitPrice == nil ||
		*result.Attached[0].LimitPrice != 109.88 {
		t.Fatalf("take profit = %+v", result.Attached[0])
	}
	if result.Attached[1].Role != "stopLoss" || result.Attached[1].Cloid != stopCloid ||
		result.Attached[1].Status != "waitingForTrigger" || result.Attached[1].TriggerPrice == nil ||
		*result.Attached[1].TriggerPrice != 90 || result.Attached[1].LimitPrice == nil ||
		*result.Attached[1].LimitPrice != 89.876 {
		t.Fatalf("stop loss = %+v", result.Attached[1])
	}
	encoded, err := json.Marshal(result.Attached)
	if err != nil {
		t.Fatal(err)
	}
	var attachedJSON []map[string]any
	if err := json.Unmarshal(encoded, &attachedJSON); err != nil {
		t.Fatal(err)
	}
	for _, attached := range attachedJSON {
		if _, exists := attached["price"]; exists {
			t.Fatalf("attached order exposes ambiguous price field: %s", encoded)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeActions) != 1 {
		t.Fatalf("actions = %d", len(fake.exchangeActions))
	}
	action := fake.exchangeActions[0]
	if action["grouping"] != "normalTpsl" {
		t.Fatalf("grouping = %v", action["grouping"])
	}
	orders, ok := action["orders"].([]any)
	if !ok || len(orders) != 3 {
		t.Fatalf("orders = %#v", action["orders"])
	}
	parent := orders[0].(map[string]any)
	if parent["a"] != float64(110000) || parent["p"] != "105" || parent["c"] != parentCloid {
		t.Fatalf("market parent = %#v", parent)
	}
	parentType := parent["t"].(map[string]any)["limit"].(map[string]any)
	if parentType["tif"] != "Ioc" {
		t.Fatalf("parent type = %#v", parentType)
	}
	want := []struct {
		index    int
		typeName string
		trigger  string
		limit    string
		cloid    string
	}{
		{1, "tp", "110", "109.88", takeCloid},
		{2, "sl", "90", "89.876", stopCloid},
	}
	for _, expected := range want {
		order := orders[expected.index].(map[string]any)
		trigger := order["t"].(map[string]any)["trigger"].(map[string]any)
		if order["p"] != expected.limit || order["r"] != true || order["c"] != expected.cloid ||
			trigger["tpsl"] != expected.typeName || trigger["triggerPx"] != expected.trigger {
			t.Errorf("child %d = %#v", expected.index, order)
		}
	}
}

func TestPlaceOrderValidatesAttachmentsBeforeExchange(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)
	price, trigger := 100.0, 110.0
	zero, above, below := 0.0, 111.0, 109.0
	valid := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pointer := func(value string) *string { return &value }
	tests := []struct {
		name   string
		params PlaceOrderParams
	}{
		{
			name: "wrong prefix",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				ParentClientOrderID: pointer("0Xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			},
		},
		{
			name: "non hexadecimal",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				ParentClientOrderID: pointer("0xgggggggggggggggggggggggggggggggg"),
			},
		},
		{
			name: "missing take profit id",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				TakeProfit: &trigger,
			},
		},
		{
			name: "stray take profit id",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				TakeProfitClientOrderID: &valid,
			},
		},
		{
			name: "stray take profit limit",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				TakeProfitLimitPrice: &trigger,
			},
		},
		{
			name: "missing stop loss id",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				StopLoss: &trigger,
			},
		},
		{
			name: "stray stop loss id",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				StopLossClientOrderID: &valid,
			},
		},
		{
			name: "stray stop loss limit",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				StopLossLimitPrice: &trigger,
			},
		},
		{
			name: "nonpositive child limit",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				TakeProfit: &trigger, TakeProfitLimitPrice: &zero, TakeProfitClientOrderID: &valid,
			},
		},
		{
			name: "sell exit limit above trigger",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				TakeProfit: &trigger, TakeProfitLimitPrice: &above, TakeProfitClientOrderID: &valid,
			},
		},
		{
			name: "buy exit limit below trigger",
			params: PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "sell", Amount: 1, Price: &price,
				StopLoss: &trigger, StopLossLimitPrice: &below, StopLossClientOrderID: &valid,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.PlaceOrder(context.Background(), test.params); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeActions) != 0 {
		t.Fatalf("invalid client order IDs sent %d exchange actions", len(fake.exchangeActions))
	}
}

func TestNotionalLimitStopsBeforeExchange(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)
	client.maxNotional = 50
	price := 100.0
	_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
	})
	if err == nil {
		t.Fatal("expected max-notional error")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeActions) != 0 {
		t.Fatal("rejected order reached /exchange")
	}
}

func TestMarketNotionalUsesConservativeWirePrice(t *testing.T) {
	fake := newFakeHyperliquid(t)
	client := newTestClient(t, fake)
	client.maxNotional = 102
	_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "wire notional $105.00") {
		t.Fatalf("error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeActions) != 0 {
		t.Fatal("wire-notional rejection reached /exchange")
	}
}

func TestAboveCapReduceOnlyRequiresVerifiedClose(t *testing.T) {
	t.Run("verified long close is exempt", func(t *testing.T) {
		fake := newFakeHyperliquid(t)
		fake.exchangeReply = map[string]any{
			"status": "ok",
			"response": map[string]any{
				"type": "order",
				"data": map[string]any{"statuses": []any{
					map[string]any{"resting": map[string]any{"oid": 123}},
				}},
			},
		}
		client := newTestClient(t, fake)
		client.maxNotional = 50
		price := 100.0
		result, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
			Symbol: "XYZ-GOLD/USDC:USDC", Side: "sell", Amount: 1, Price: &price, ReduceOnly: true,
		})
		if err != nil || result.Order == nil || result.Order.ID != "123" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})

	for _, test := range []struct {
		name   string
		side   string
		amount float64
	}{
		{name: "wrong direction", side: "buy", amount: 1},
		{name: "oversize", side: "sell", amount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			client := newTestClient(t, fake)
			client.maxNotional = 50
			price := 100.0
			_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: test.side, Amount: test.amount,
				Price: &price, ReduceOnly: true,
			})
			if err == nil || !strings.Contains(err.Error(), "reduce-only exemption requires") {
				t.Fatalf("error = %v", err)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.exchangeActions) != 0 {
				t.Fatal("unverified reduce-only order reached /exchange")
			}
		})
	}
}

func TestConcurrentNoncesAreUnique(t *testing.T) {
	fake := newFakeHyperliquid(t)
	fake.exchangeReply = map[string]any{
		"status": "ok",
		"response": map[string]any{
			"type": "order",
			"data": map[string]any{"statuses": []any{map[string]any{"resting": map[string]any{"oid": 123}}}},
		},
	}
	client := newTestClient(t, fake)
	price := 100.0
	const count = 40
	var wait sync.WaitGroup
	errorsFound := make(chan error, count)
	wait.Add(count)
	for range count {
		go func() {
			defer wait.Done()
			_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
			})
			if err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.exchangeActions) != count {
		t.Fatalf("exchange calls = %d", len(fake.exchangeActions))
	}
	seen := make(map[uint64]struct{}, count)
	for _, nonce := range fake.exchangeNonces {
		if _, exists := seen[nonce]; exists {
			t.Fatal("duplicate nonce")
		}
		seen[nonce] = struct{}{}
	}
}

func TestPriceAndSizePrecision(t *testing.T) {
	tests := []struct {
		price float64
		szDec int
		want  string
	}{{4423.15, 4, "4423.2"}, {100, 2, "100"}, {0.123456, 5, "0.1"}}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%g", test.price), func(t *testing.T) {
			got, _, err := formatPrice(test.price, test.szDec)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
	if _, err := formatSize(0.123, 2); err == nil {
		t.Fatal("expected excess-size-precision error")
	}
}

func TestCancelOrderRequiresOneWellFormedCancelStatus(t *testing.T) {
	tests := []struct {
		name      string
		response  any
		wantState string
		ambiguous bool
	}{
		{
			name: "success",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{"success"}},
			},
			wantState: "succeeded",
		},
		{
			name: "explicit rejection",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{map[string]any{"error": "MissingOrder"}}},
			},
			wantState: "failed",
		},
		{
			name: "wrong response type",
			response: map[string]any{
				"type": "order", "data": map[string]any{"statuses": []any{"success"}},
			},
			wantState: "unknown", ambiguous: true,
		},
		{
			name: "missing status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{}},
			},
			wantState: "unknown", ambiguous: true,
		},
		{
			name: "extra status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{"success", "success"}},
			},
			wantState: "unknown", ambiguous: true,
		},
		{
			name: "malformed status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{123}},
			},
			wantState: "unknown", ambiguous: true,
		},
		{
			name: "unexpected string status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{"waitingForFill"}},
			},
			wantState: "unknown", ambiguous: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			fake.exchangeReply = map[string]any{"status": "ok", "response": test.response}
			client := newTestClient(t, fake)
			result, err := client.CancelOrder(context.Background(), "77", "XYZ-GOLD/USDC:USDC")
			if result.Status != test.wantState {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if test.wantState == "succeeded" && err != nil {
				t.Fatal(err)
			}
			if test.wantState == "failed" && err == nil {
				t.Fatal("expected explicit cancel rejection")
			}
			if test.ambiguous {
				var mutationErr *MutationError
				if !errors.As(err, &mutationErr) || !mutationErr.Ambiguous {
					t.Fatalf("error = %#v", err)
				}
			}
		})
	}
}

func TestCancelAllTreatsMalformedOrMismatchedStatusesAsUnknown(t *testing.T) {
	tests := []struct {
		name     string
		response any
	}{
		{
			name: "wrong response type",
			response: map[string]any{
				"type": "order", "data": map[string]any{"statuses": []any{"success"}},
			},
		},
		{
			name: "missing status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{}},
			},
		},
		{
			name: "extra status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{"success", "success"}},
			},
		},
		{
			name: "malformed status",
			response: map[string]any{
				"type": "cancel", "data": map[string]any{"statuses": []any{map[string]any{"resting": map[string]any{"oid": 77}}}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			fake.exchangeReply = map[string]any{"status": "ok", "response": test.response}
			client := newTestClient(t, fake)
			result, err := client.CancelAll(context.Background(), "XYZ-GOLD/USDC:USDC")
			var mutationErr *MutationError
			if result.Status != "unknown" || !errors.As(err, &mutationErr) || !mutationErr.Ambiguous {
				t.Fatalf("result = %+v, error = %#v", result, err)
			}
		})
	}
}

func TestCancelAllClassifiesMatchedStatuses(t *testing.T) {
	result := MutationResult{
		Status: "succeeded",
		Raw: json.RawMessage(
			`{"status":"ok","response":{"type":"cancel","data":{"statuses":["success",{"error":"MissingOrder"}]}}}`,
		),
	}
	checked, err := checkCancelStatuses(result, nil, 2)
	var mutationErr *MutationError
	if checked.Status != "partial" || !errors.As(err, &mutationErr) || !mutationErr.Partial {
		t.Fatalf("result = %+v, error = %#v", checked, err)
	}
}

func TestMalformedExchangeEnvelopeStatusMakesPlaceOrderUnknown(t *testing.T) {
	for _, test := range []struct {
		name  string
		reply map[string]any
	}{
		{
			name: "missing",
			reply: map[string]any{
				"response": map[string]any{
					"type": "order",
					"data": map[string]any{"statuses": []any{map[string]any{"resting": map[string]any{"oid": 1}}}},
				},
			},
		},
		{
			name: "null",
			reply: map[string]any{
				"status": nil,
				"response": map[string]any{
					"type": "order",
					"data": map[string]any{"statuses": []any{map[string]any{"resting": map[string]any{"oid": 1}}}},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			fake.exchangeReply = test.reply
			client := newTestClient(t, fake)
			price := 100.0
			result, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
			})
			var mutationErr *MutationError
			if !errors.As(err, &mutationErr) || !mutationErr.Ambiguous || result.Status != "unknown" ||
				result.Order == nil || result.Order.Status != "unknown" {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
		})
	}
}

func TestMalformedAndWrongCountOrderResponsesAreAmbiguous(t *testing.T) {
	legs := []orderLeg{{Role: "parent", Cloid: "parent"}, {Role: "takeProfit", Cloid: "take"}}
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "malformed envelope", raw: json.RawMessage(`{`)},
		{name: "malformed response", raw: json.RawMessage(`{"status":"ok","response":"bad"}`)},
		{name: "wrong type", raw: json.RawMessage(`{"status":"ok","response":{"type":"default","data":{"statuses":[]}}}`)},
		{name: "missing status", raw: json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1}}]}}}`)},
		{name: "extra status", raw: json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1}},"waitingForFill","success"]}}}`)},
		{name: "malformed status", raw: json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1}},123]}}}`)},
		{name: "conflicting status", raw: json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1}},{"resting":{"oid":2},"filled":{"oid":2}}]}}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOrderStatuses(test.raw, legs)
			var mutationErr *MutationError
			if !errors.As(err, &mutationErr) || !mutationErr.Ambiguous {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDefaultMutationResponseRequiresExactTypeAndNoStatuses(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		ok   bool
	}{
		{name: "default", raw: json.RawMessage(`{"status":"ok","response":{"type":"default"}}`), ok: true},
		{name: "wrong type", raw: json.RawMessage(`{"status":"ok","response":{"type":"cancel"}}`)},
		{name: "malformed response", raw: json.RawMessage(`{"status":"ok","response":null}`)},
		{name: "unexpected statuses", raw: json.RawMessage(`{"status":"ok","response":{"type":"default","data":{"statuses":["success"]}}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := checkDefaultMutationResponse(MutationResult{Status: "succeeded", Raw: test.raw}, nil)
			if test.ok {
				if err != nil || result.Status != "succeeded" {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
				return
			}
			var mutationErr *MutationError
			if !errors.As(err, &mutationErr) || !mutationErr.Ambiguous || result.Status != "unknown" {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
		})
	}
}

func TestWaitingForFillIsPreserved(t *testing.T) {
	raw := json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1}},"waitingForFill"]}}}`)
	statuses, err := parseOrderStatuses(raw, []orderLeg{
		{Role: "parent", Cloid: "parent"},
		{Role: "takeProfit", Cloid: "take"},
	})
	if err != nil || len(statuses) != 2 || statuses[1].Status != "waitingForFill" || statuses[1].Cloid != "take" {
		t.Fatalf("statuses = %+v, error = %v", statuses, err)
	}

}

func TestPlaceOrderClassifiesWrongCountUnknownAndExplicitChildFailurePartial(t *testing.T) {
	for _, test := range []struct {
		name       string
		child      any
		wantStatus string
		ambiguous  bool
		partial    bool
	}{
		{name: "missing child status", child: nil, wantStatus: "unknown", ambiguous: true},
		{name: "explicit child rejection", child: map[string]any{"error": "bad trigger"}, wantStatus: "partial", partial: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			statuses := []any{map[string]any{"resting": map[string]any{"oid": 123}}}
			if test.child != nil {
				statuses = append(statuses, test.child)
			}
			fake.exchangeReply = map[string]any{
				"status": "ok",
				"response": map[string]any{
					"type": "order", "data": map[string]any{"statuses": statuses},
				},
			}
			client := newTestClient(t, fake)
			price, take := 100.0, 110.0
			cloid := "0x22222222222222222222222222222222"
			result, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
				Symbol: "XYZ-GOLD/USDC:USDC", Side: "buy", Amount: 1, Price: &price,
				TakeProfit: &take, TakeProfitClientOrderID: &cloid,
			})
			var mutationErr *MutationError
			if !errors.As(err, &mutationErr) || mutationErr.Ambiguous != test.ambiguous ||
				mutationErr.Partial != test.partial || result.Status != test.wantStatus {
				t.Fatalf("result = %+v, error = %#v", result, mutationErr)
			}
			if test.ambiguous && (result.Order == nil || result.Order.Status != "unknown") {
				t.Fatalf("order = %+v", result.Order)
			}
		})
	}
}

func TestSizePrecisionDoesNotUseAmountScaledTolerance(t *testing.T) {
	if _, err := formatSize(1_000_000_000.001, 0); err == nil {
		t.Fatal("expected excess precision error")
	}
	if value, err := formatSize(1.23, 2); err != nil || value != "1.23" {
		t.Fatalf("valid amount = %q, %v", value, err)
	}
}

func TestCancelAllChecksEachExchangeStatus(t *testing.T) {
	fake := newFakeHyperliquid(t)
	fake.exchangeReply = map[string]any{
		"status": "ok",
		"response": map[string]any{
			"type": "cancel",
			"data": map[string]any{"statuses": []any{map[string]any{"error": "order was not found"}}},
		},
	}
	client := newTestClient(t, fake)
	result, err := client.CancelAll(context.Background(), "XYZ-GOLD/USDC:USDC")
	if err == nil || result.Status != "failed" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestSubmitClassifiesEnvelopeStatuses(t *testing.T) {
	for _, test := range []struct {
		name          string
		reply         map[string]any
		wantErr       string
		wantAmbiguous bool
		wantStatus    string
	}{
		{
			name: "err status is a definite rejection",
			reply: map[string]any{
				"status": "err", "response": "order price must be positive",
			},
			wantErr:    "Hyperliquid rejected action: order price must be positive",
			wantStatus: "failed",
		},
		{
			name: "unknown status wording stays ambiguous",
			reply: map[string]any{
				"status": "success", "response": map[string]any{"type": "default"},
			},
			wantErr:       "exchange outcome unknown; unexpected status success",
			wantAmbiguous: true,
			wantStatus:    "unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeHyperliquid(t)
			fake.mu.Lock()
			fake.exchangeReply = test.reply
			fake.mu.Unlock()
			client, err := NewClient(Options{
				BaseURL: fake.server.URL, WalletAddress: "0x1111111111111111111111111111111111111111",
				PrivateKey: vectorPrivateKey, MaxNotional: 1_000_000, Timeout: 2 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			action := CancelAction{Type: "cancel", Cancels: []CancelWire{{Asset: 1, OID: 42}}}
			result, err := client.submit(context.Background(), action, MutationOptions{
				Nonce: client.ReserveNonce(),
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
			var mutationErr *MutationError
			ambiguous := errors.As(err, &mutationErr) && mutationErr.Ambiguous
			if ambiguous != test.wantAmbiguous {
				t.Fatalf("error = %v, want ambiguous = %t", err, test.wantAmbiguous)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, test.wantStatus)
			}
		})
	}
}
