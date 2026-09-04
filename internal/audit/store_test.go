package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestReserveFenceAllocatesAndValidatesExactCurrentGeneration(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	const venue = "hyperliquid:mainnet:0xabc"
	expiresAtMs := time.Now().Add(time.Minute).UnixMilli()
	ownerGeneration := int64(91)

	first, err := store.ReserveFence(ctx, venue, expiresAtMs, &ownerGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || first.ExpiresAtMs != expiresAtMs ||
		first.OwnerGeneration == nil || *first.OwnerGeneration != ownerGeneration {
		t.Fatalf("first reservation = %+v", first)
	}
	if err := store.ValidateFence(ctx, venue, first.Generation, expiresAtMs); err != nil {
		t.Fatalf("validate current reservation: %v", err)
	}
	if err := store.ValidateFence(ctx, venue+":missing", 1, expiresAtMs); !errors.Is(err, ErrFenceNotReserved) {
		t.Fatalf("unreserved fence error = %v", err)
	}
	if err := store.ValidateFence(ctx, venue, 2, expiresAtMs); !errors.Is(err, ErrFenceGenerationMismatch) {
		t.Fatalf("newer unreserved fence error = %v", err)
	}
	if err := store.ValidateFence(ctx, venue, 1, expiresAtMs+1); !errors.Is(err, ErrFenceExpirationMismatch) {
		t.Fatalf("expiration mismatch error = %v", err)
	}

	second, err := store.ReserveFence(ctx, venue, expiresAtMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != 2 {
		t.Fatalf("second generation = %d, want 2", second.Generation)
	}
	if err := store.ValidateFence(ctx, venue, first.Generation, expiresAtMs); !errors.Is(err, ErrFenceGenerationMismatch) {
		t.Fatalf("stale fence error = %v", err)
	}
	if err := store.ValidateFence(ctx, venue, second.Generation, expiresAtMs); err != nil {
		t.Fatalf("validate second reservation: %v", err)
	}

	var storedOwner sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `
		SELECT owner_generation
		FROM mutation_fence_reservations
		WHERE venue_key = ? AND generation = 1`, venue,
	).Scan(&storedOwner); err != nil {
		t.Fatal(err)
	}
	if !storedOwner.Valid || storedOwner.Int64 != ownerGeneration {
		t.Fatalf("stored owner generation = %+v", storedOwner)
	}
}

func TestReserveFenceConcurrentCallersGetUniqueGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	firstStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()

	ctx := context.Background()
	const venue = "hyperliquid:mainnet:0xconcurrent"
	const callerCount = 32
	expiresAtMs := time.Now().Add(time.Minute).UnixMilli()
	results := make(chan FenceReservation, callerCount)
	errorsByCaller := make(chan error, callerCount)
	var wait sync.WaitGroup
	for caller := int64(1); caller <= callerCount; caller++ {
		wait.Add(1)
		go func(ownerGeneration int64) {
			defer wait.Done()
			store := firstStore
			if ownerGeneration%2 == 0 {
				store = secondStore
			}
			reservation, err := store.ReserveFence(ctx, venue, expiresAtMs, &ownerGeneration)
			if err != nil {
				errorsByCaller <- err
				return
			}
			results <- reservation
		}(caller)
	}
	wait.Wait()
	close(results)
	close(errorsByCaller)
	for err := range errorsByCaller {
		t.Error(err)
	}
	var generations []int
	for reservation := range results {
		generations = append(generations, int(reservation.Generation))
	}
	if len(generations) != callerCount {
		t.Fatalf("reservations = %d, want %d", len(generations), callerCount)
	}
	sort.Ints(generations)
	for index, generation := range generations {
		if generation != index+1 {
			t.Fatalf("generations = %v", generations)
		}
	}
	if err := firstStore.ValidateFence(ctx, venue, callerCount, expiresAtMs); err != nil {
		t.Fatalf("validate last concurrent reservation: %v", err)
	}
	var auditCount int
	if err := firstStore.db.QueryRowContext(ctx, `
		SELECT count(*) FROM mutation_fence_reservations WHERE venue_key = ?`, venue,
	).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != callerCount {
		t.Fatalf("reservation audit rows = %d, want %d", auditCount, callerCount)
	}
}

func TestReserveFencePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const venue = "hyperliquid:testnet:0xabc"
	expiresAtMs := time.Now().Add(time.Minute).UnixMilli()
	first, err := store.ReserveFence(context.Background(), venue, expiresAtMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second, err := reopened.ReserveFence(context.Background(), venue, expiresAtMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != 1 || second.Generation != 2 {
		t.Fatalf("restart generations = %d, %d", first.Generation, second.Generation)
	}
}

func TestReserveFenceFailsClosedAtInt64Exhaustion(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation any
	}{
		{name: "maximum integer", generation: int64(math.MaxInt64)},
		{name: "legacy overflowing real", generation: float64(math.MaxInt64) * 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			const venue = "hyperliquid:mainnet:0xexhausted"
			expiresAtMs := time.Now().Add(time.Minute).UnixMilli()
			if _, err := store.db.Exec(`
				INSERT INTO mutation_fences (
					venue_key, generation, expires_at_ms, owner_generation, updated_at_ms
				) VALUES (?, ?, ?, NULL, ?)`,
				venue, test.generation, expiresAtMs, time.Now().UnixMilli(),
			); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReserveFence(
				context.Background(), venue, expiresAtMs, nil,
			); !errors.Is(err, ErrFenceGenerationExhausted) {
				t.Fatalf("ReserveFence() error = %v, want exhaustion", err)
			}
			var count int
			if err := store.db.QueryRow(`
				SELECT count(*) FROM mutation_fence_reservations WHERE venue_key = ?`, venue,
			).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("reservation audit rows = %d, want 0", count)
			}
		})
	}
}

func TestStoreLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	amount, price, stop := 0.25, 100.5, 95.0
	id, err := store.Start(context.Background(), StartEvent{
		Action: "place_order", Symbol: "BTC/USDC:USDC", Side: "buy", OrderType: "limit",
		Amount: &amount, RequestedPrice: &price, StopLoss: &stop,
		VenueKey: "hyperliquid:mainnet:0xabc", Nonce: 42,
		Request: json.RawMessage(`{"symbol":"BTC/USDC:USDC"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), id, Completion{
		Status: "succeeded", ExchangeOrderID: "123",
		Response: json.RawMessage(`{"status":"ok"}`), Latency: 1500 * time.Microsecond,
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.List(context.Background(), Filter{Limit: 10, IncludeResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events", len(events))
	}
	event := events[0]
	if event.ID != id || event.Status != "succeeded" || event.ExchangeOrderID != "123" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.Amount == nil || *event.Amount != amount || event.LatencyMS == nil || *event.LatencyMS != 1.5 {
		t.Fatalf("numeric fields lost: %+v", event)
	}
	if string(event.Response) != `{"status":"ok"}` {
		t.Fatalf("response = %s", event.Response)
	}
}

func TestStoreLeavesPendingAttemptVisible(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Start(context.Background(), testStartEvent("cancel_all", "ETH/USDC:USDC")); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Status != "pending" || events[0].CompletedAt != "" {
		t.Fatalf("unexpected pending event: %+v", events)
	}
}

func TestStoreMarksPendingUnknownAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Start(context.Background(), testStartEvent("cancel_all", "ETH/USDC:USDC")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.List(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Status != "unknown" || events[0].Error == "" {
		t.Fatalf("events = %+v", events)
	}
}

func TestStoreUsesWALAndFilterIndexes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q", journalMode)
	}
	var synchronous int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous = %d, want FULL (2)", synchronous)
	}
	rows, err := store.db.Query("PRAGMA index_list('trade_events')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexes := map[string]bool{}
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	for _, name := range []string{"trade_events_symbol_id", "trade_events_action_id", "trade_events_status_id"} {
		if !indexes[name] {
			t.Errorf("missing index %s", name)
		}
	}
}

func TestCrashRecoveryPreservesReconciliationIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	event := testStartEvent("place_order", "BTC/USDC:USDC")
	event.Nonce = 1700000000123
	event.OperationIdentifiers = []OperationIdentifier{
		{Kind: "cloid", Role: "parent", Value: "0x11111111111111111111111111111111"},
		{Kind: "cloid", Role: "takeProfit", Value: "0x22222222222222222222222222222222"},
		{Kind: "cloid", Role: "stopLoss", Value: "0x33333333333333333333333333333333"},
	}
	event.Request = json.RawMessage(`{"symbol":"BTC/USDC:USDC","side":"buy","amount":0.1}`)
	id, err := store.Start(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	venueRequest := json.RawMessage(`{"type":"order","orders":[{"a":0,"c":"0x11111111111111111111111111111111"}]}`)
	if err := store.RecordVenueRequest(context.Background(), id, venueRequest); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.List(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	recovered := events[0]
	if recovered.Status != "unknown" || recovered.Nonce == nil || *recovered.Nonce != event.Nonce {
		t.Fatalf("recovered status or nonce = %+v", recovered)
	}
	if recovered.VenueKey != event.VenueKey || string(recovered.Request) != string(event.Request) ||
		string(recovered.VenueRequest) != string(venueRequest) {
		t.Fatalf("reconciliation request identity lost: %+v", recovered)
	}
	if len(recovered.OperationIdentifiers) != 3 || recovered.OperationIdentifiers[2].Role != "stopLoss" {
		t.Fatalf("operation identifiers = %+v", recovered.OperationIdentifiers)
	}
}

func TestOpenMigratesLegacyAuditDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE mutation_fences (
			venue_key TEXT PRIMARY KEY,
			generation INTEGER NOT NULL CHECK (generation > 0),
			updated_at_ms INTEGER NOT NULL
		);
		INSERT INTO mutation_fences (venue_key, generation, updated_at_ms)
		VALUES ('hyperliquid:mainnet:0xlegacy', 9, 1700000000000);
		CREATE TABLE trade_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at_ms INTEGER NOT NULL,
			completed_at_ms INTEGER,
			action TEXT NOT NULL,
			symbol TEXT NOT NULL,
			side TEXT,
			order_type TEXT,
			amount TEXT,
			requested_price TEXT,
			stop_loss TEXT,
			take_profit TEXT,
			reduce_only INTEGER NOT NULL DEFAULT 0,
			exchange_order_id TEXT,
			nonce INTEGER,
			status TEXT NOT NULL,
			error TEXT,
			latency_us INTEGER,
			response_json TEXT
		);
		INSERT INTO trade_events (created_at_ms, action, symbol, reduce_only, nonce, status)
		VALUES (1700000000000, 'cancel_order', 'BTC/USDC:USDC', 0, 99, 'pending')
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, column := range []string{
		"venue_key", "operation_identifiers_json", "request_json", "venue_request_json",
	} {
		var count int
		if err := store.db.QueryRow(
			"SELECT count(*) FROM pragma_table_info('trade_events') WHERE name = ?", column,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("column %s count = %d", column, count)
		}
	}
	for _, column := range []string{"expires_at_ms", "owner_generation"} {
		var count int
		if err := store.db.QueryRow(
			"SELECT count(*) FROM pragma_table_info('mutation_fences') WHERE name = ?", column,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("mutation fence column %s count = %d", column, count)
		}
	}
	expiresAtMs := time.Now().Add(time.Minute).UnixMilli()
	reservation, err := store.ReserveFence(
		context.Background(), "hyperliquid:mainnet:0xlegacy", expiresAtMs, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Generation != 10 {
		t.Fatalf("migrated reservation generation = %d, want 10", reservation.Generation)
	}
	events, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Status != "unknown" || events[0].Nonce == nil || *events[0].Nonce != 99 {
		t.Fatalf("legacy events = %+v", events)
	}
}

func testStartEvent(action, symbol string) StartEvent {
	return StartEvent{
		Action:   action,
		Symbol:   symbol,
		VenueKey: "hyperliquid:testnet:0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Nonce:    1,
		Request:  json.RawMessage(`{"symbol":"` + symbol + `"}`),
	}
}

func BenchmarkWriteLifecycle(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "audit.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, err := store.Start(ctx, testStartEvent("place_order", "BTC/USDC:USDC"))
		if err != nil {
			b.Fatal(err)
		}
		if err := store.Complete(ctx, id, Completion{Status: "succeeded"}); err != nil {
			b.Fatal(err)
		}
	}
}
