package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var (
	// ErrFenceNotReserved means the venue account has no writer-reserved generation.
	ErrFenceNotReserved = errors.New("mutation fence is not reserved")
	// ErrFenceGenerationMismatch means the requested generation is not current.
	ErrFenceGenerationMismatch = errors.New("mutation fence generation is not current")
	// ErrFenceExpirationMismatch means the requested expiry differs from its reservation.
	ErrFenceExpirationMismatch = errors.New("mutation fence expiration does not match current reservation")
	// ErrFenceGenerationExhausted means the venue account reached SQLite's largest integer.
	ErrFenceGenerationExhausted = errors.New("mutation fence generation exhausted")
)

type Store struct {
	db           *sql.DB
	sendLockPath string
}

type OperationIdentifier struct {
	Kind  string `json:"kind"`
	Role  string `json:"role,omitempty"`
	Value string `json:"value"`
}

type StartEvent struct {
	Action               string
	Symbol               string
	Side                 string
	OrderType            string
	Amount               *float64
	RequestedPrice       *float64
	StopLoss             *float64
	TakeProfit           *float64
	ReduceOnly           bool
	ExchangeOrderID      string
	VenueKey             string
	Nonce                uint64
	OperationIdentifiers []OperationIdentifier
	Request              json.RawMessage
}

type Completion struct {
	Status          string
	ExchangeOrderID string
	Response        json.RawMessage
	Error           string
	Latency         time.Duration
}

type Filter struct {
	Limit           int
	BeforeID        int64
	Symbol          string
	Action          string
	Status          string
	IncludeResponse bool
}

type Event struct {
	ID                   int64                 `json:"id"`
	CreatedAt            string                `json:"createdAt"`
	CompletedAt          string                `json:"completedAt,omitempty"`
	Action               string                `json:"action"`
	Symbol               string                `json:"symbol"`
	Side                 string                `json:"side,omitempty"`
	OrderType            string                `json:"orderType,omitempty"`
	Amount               *float64              `json:"amount,omitempty"`
	RequestedPrice       *float64              `json:"requestedPrice,omitempty"`
	StopLoss             *float64              `json:"stopLoss,omitempty"`
	TakeProfit           *float64              `json:"takeProfit,omitempty"`
	ReduceOnly           bool                  `json:"reduceOnly"`
	ExchangeOrderID      string                `json:"exchangeOrderId,omitempty"`
	VenueKey             string                `json:"venueKey,omitempty"`
	Nonce                *uint64               `json:"nonce,omitempty"`
	OperationIdentifiers []OperationIdentifier `json:"operationIdentifiers,omitempty"`
	Request              json.RawMessage       `json:"request,omitempty"`
	VenueRequest         json.RawMessage       `json:"venueRequest,omitempty"`
	Status               string                `json:"status"`
	Error                string                `json:"error,omitempty"`
	LatencyMS            *float64              `json:"latencyMs,omitempty"`
	Response             json.RawMessage       `json:"response,omitempty"`
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsnURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := dsnURL.Query()
	for _, pragma := range []string{
		"busy_timeout(1000)",
		"foreign_keys(ON)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
	} {
		query.Add("_pragma", pragma)
	}
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open SQLite audit database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	store := &Store{db: db, sendLockPath: absolute + ".send.lock"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping SQLite audit database: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.recoverPending(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("protect SQLite audit database: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

type FenceReservation struct {
	Generation      int64
	ExpiresAtMs     int64
	OwnerGeneration *int64
}

// AcquireSendLock takes the cross-process mutation send lock. Reservation and
// the final pre-send fence recheck both hold it, so a newer reservation can
// never interleave between an older request's gate check and its network send.
// The returned release function must run after the venue request completes.
// Waiting respects ctx: a canceled caller abandons the lock without blocking
// the process.
func (s *Store) AcquireSendLock(ctx context.Context) (func(), error) {
	file, err := os.OpenFile(s.sendLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open mutation send lock: %w", err)
	}
	type result struct {
		release func()
		err     error
	}
	done := make(chan result, 1)
	go func() {
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
			file.Close()
			done <- result{err: fmt.Errorf("lock mutation send lock: %w", err)}
			return
		}
		var once sync.Once
		done <- result{release: func() {
			once.Do(func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			})
		}}
	}()
	select {
	case got := <-done:
		return got.release, got.err
	case <-ctx.Done():
		go func() {
			if got := <-done; got.release != nil {
				got.release()
			}
		}()
		return nil, fmt.Errorf("acquire mutation send lock: %w", ctx.Err())
	}
}

// ReserveFence atomically allocates the next venue-account generation.
func (s *Store) ReserveFence(
	ctx context.Context, venueKey string, expiresAtMs int64, ownerGeneration *int64,
) (FenceReservation, error) {
	if strings.TrimSpace(venueKey) == "" {
		return FenceReservation{}, errors.New("fence venue key is required")
	}
	if expiresAtMs <= 0 {
		return FenceReservation{}, errors.New("fence expiration must be positive")
	}
	if ownerGeneration != nil && *ownerGeneration <= 0 {
		return FenceReservation{}, errors.New("fence owner generation must be positive")
	}

	release, err := s.AcquireSendLock(ctx)
	if err != nil {
		return FenceReservation{}, err
	}
	defer release()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FenceReservation{}, fmt.Errorf("begin mutation fence reservation: %w", err)
	}
	defer tx.Rollback()

	reservedAtMs := time.Now().UTC().UnixMilli()
	var generation int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO mutation_fences (
			venue_key, generation, expires_at_ms, owner_generation, updated_at_ms
		) VALUES (?, 1, ?, ?, ?)
		ON CONFLICT (venue_key) DO UPDATE SET
			generation = mutation_fences.generation + 1,
			expires_at_ms = excluded.expires_at_ms,
			owner_generation = excluded.owner_generation,
			updated_at_ms = excluded.updated_at_ms
		WHERE mutation_fences.generation < ?
		RETURNING generation`,
		venueKey, expiresAtMs, ownerGeneration, reservedAtMs, int64(math.MaxInt64),
	).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return FenceReservation{}, fmt.Errorf("%w for %s", ErrFenceGenerationExhausted, venueKey)
	}
	if err != nil {
		return FenceReservation{}, fmt.Errorf("reserve mutation fence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO mutation_fence_reservations (
			venue_key, generation, expires_at_ms, owner_generation, reserved_at_ms
		) VALUES (?, ?, ?, ?, ?)`,
		venueKey, generation, expiresAtMs, ownerGeneration, reservedAtMs,
	); err != nil {
		return FenceReservation{}, fmt.Errorf("audit mutation fence reservation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FenceReservation{}, fmt.Errorf("commit mutation fence reservation: %w", err)
	}
	return FenceReservation{
		Generation: generation, ExpiresAtMs: expiresAtMs, OwnerGeneration: ownerGeneration,
	}, nil
}

// ValidateFence requires an exact current writer reservation.
func (s *Store) ValidateFence(
	ctx context.Context, venueKey string, generation, expiresAtMs int64,
) error {
	if strings.TrimSpace(venueKey) == "" {
		return errors.New("fence venue key is required")
	}
	if generation <= 0 {
		return errors.New("fence generation must be positive")
	}
	if expiresAtMs <= 0 {
		return errors.New("fence expiration must be positive")
	}
	var currentGeneration int64
	var currentExpiresAtMs sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT generation, expires_at_ms
		FROM mutation_fences
		WHERE venue_key = ?`, venueKey,
	).Scan(&currentGeneration, &currentExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w for %s", ErrFenceNotReserved, venueKey)
	}
	if err != nil {
		return fmt.Errorf("read current mutation fence: %w", err)
	}
	if !currentExpiresAtMs.Valid {
		return fmt.Errorf("%w for %s", ErrFenceNotReserved, venueKey)
	}
	if generation != currentGeneration {
		return fmt.Errorf("%w: got %d, current %d", ErrFenceGenerationMismatch, generation, currentGeneration)
	}
	if expiresAtMs != currentExpiresAtMs.Int64 {
		return fmt.Errorf(
			"%w: got %d, reserved %d",
			ErrFenceExpirationMismatch, expiresAtMs, currentExpiresAtMs.Int64,
		)
	}
	return nil
}

func (s *Store) Start(ctx context.Context, event StartEvent) (int64, error) {
	if event.Action == "" || event.Symbol == "" || event.VenueKey == "" {
		return 0, errors.New("audit action, symbol, and venue key are required")
	}
	if event.Nonce == 0 || event.Nonce > math.MaxInt64 {
		return 0, errors.New("audit nonce must fit a positive SQLite integer")
	}
	if len(event.Request) == 0 || !json.Valid(event.Request) {
		return 0, errors.New("audit request is not valid JSON")
	}
	for _, identifier := range event.OperationIdentifiers {
		if identifier.Kind == "" || identifier.Value == "" {
			return 0, errors.New("audit operation identifier kind and value are required")
		}
	}
	identifiers, err := json.Marshal(event.OperationIdentifiers)
	if err != nil {
		return 0, fmt.Errorf("encode audit operation identifiers: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO trade_events (
			created_at_ms, action, symbol, side, order_type, amount,
			requested_price, stop_loss, take_profit, reduce_only,
			exchange_order_id, venue_key, nonce, operation_identifiers_json,
			request_json, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
		time.Now().UTC().UnixMilli(), event.Action, event.Symbol, nullText(event.Side),
		nullText(event.OrderType), numberText(event.Amount), numberText(event.RequestedPrice),
		numberText(event.StopLoss), numberText(event.TakeProfit), event.ReduceOnly,
		nullText(event.ExchangeOrderID), event.VenueKey, event.Nonce, string(identifiers),
		string(event.Request),
	)
	if err != nil {
		return 0, fmt.Errorf("start trade audit: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read trade audit id: %w", err)
	}
	return id, nil
}

func (s *Store) RecordVenueRequest(ctx context.Context, id int64, request json.RawMessage) error {
	if id <= 0 {
		return errors.New("audit id is required")
	}
	if len(request) == 0 || !json.Valid(request) {
		return errors.New("audit venue request is not valid JSON")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE trade_events
		SET venue_request_json = ?
		WHERE id = ? AND status = 'pending'`, string(request), id)
	if err != nil {
		return fmt.Errorf("record venue request before send: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read venue request audit update count: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("pending trade audit event %d was not found", id)
	}
	return nil
}

func (s *Store) Complete(ctx context.Context, id int64, completion Completion) error {
	if id <= 0 || completion.Status == "" {
		return errors.New("audit id and completion status are required")
	}
	var response any
	if len(completion.Response) != 0 {
		if !json.Valid(completion.Response) {
			return errors.New("audit response is not valid JSON")
		}
		response = string(completion.Response)
	}
	var latency any
	if completion.Latency > 0 {
		latency = completion.Latency.Microseconds()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE trade_events
		SET completed_at_ms = ?, status = ?, exchange_order_id = COALESCE(NULLIF(?, ''), exchange_order_id),
			response_json = ?, error = ?, latency_us = ?
		WHERE id = ?`,
		time.Now().UTC().UnixMilli(), completion.Status, completion.ExchangeOrderID,
		response, nullText(completion.Error), latency, id,
	)
	if err != nil {
		return fmt.Errorf("complete trade audit: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read trade audit update count: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("trade audit event %d was not found", id)
	}
	return nil
}

func (s *Store) List(ctx context.Context, filter Filter) ([]Event, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 500 {
		return nil, errors.New("limit must be from 1 through 500")
	}
	conditions := []string{"1 = 1"}
	var arguments []any
	if filter.BeforeID > 0 {
		conditions = append(conditions, "id < ?")
		arguments = append(arguments, filter.BeforeID)
	}
	if filter.Symbol != "" {
		conditions = append(conditions, "symbol = ?")
		arguments = append(arguments, filter.Symbol)
	}
	if filter.Action != "" {
		conditions = append(conditions, "action = ?")
		arguments = append(arguments, filter.Action)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		arguments = append(arguments, filter.Status)
	}
	arguments = append(arguments, filter.Limit)
	query := `
		SELECT id, created_at_ms, completed_at_ms, action, symbol, side, order_type,
			amount, requested_price, stop_loss, take_profit, reduce_only,
			exchange_order_id, venue_key, nonce, operation_identifiers_json,
			request_json, venue_request_json, status, error, latency_us, response_json
		FROM trade_events
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY id DESC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list trade audit: %w", err)
	}
	defer rows.Close()

	result := make([]Event, 0, filter.Limit)
	for rows.Next() {
		var event Event
		var created int64
		var completed, nonce, latency sql.NullInt64
		var side, orderType, amount, price, stopLoss, takeProfit sql.NullString
		var exchangeOrderID, venueKey, identifiers, request, venueRequest sql.NullString
		var errorText, response sql.NullString
		if err := rows.Scan(
			&event.ID, &created, &completed, &event.Action, &event.Symbol, &side, &orderType,
			&amount, &price, &stopLoss, &takeProfit, &event.ReduceOnly,
			&exchangeOrderID, &venueKey, &nonce, &identifiers, &request, &venueRequest,
			&event.Status, &errorText, &latency, &response,
		); err != nil {
			return nil, fmt.Errorf("scan trade audit: %w", err)
		}
		event.CreatedAt = time.UnixMilli(created).UTC().Format(time.RFC3339Nano)
		if completed.Valid {
			event.CompletedAt = time.UnixMilli(completed.Int64).UTC().Format(time.RFC3339Nano)
		}
		event.Side = side.String
		event.OrderType = orderType.String
		event.Amount = parseNumber(amount)
		event.RequestedPrice = parseNumber(price)
		event.StopLoss = parseNumber(stopLoss)
		event.TakeProfit = parseNumber(takeProfit)
		event.ExchangeOrderID = exchangeOrderID.String
		event.VenueKey = venueKey.String
		event.Error = errorText.String
		if identifiers.Valid && json.Valid([]byte(identifiers.String)) {
			_ = json.Unmarshal([]byte(identifiers.String), &event.OperationIdentifiers)
		}
		if request.Valid && json.Valid([]byte(request.String)) {
			event.Request = json.RawMessage(request.String)
		}
		if venueRequest.Valid && json.Valid([]byte(venueRequest.String)) {
			event.VenueRequest = json.RawMessage(venueRequest.String)
		}
		if nonce.Valid {
			value := uint64(nonce.Int64)
			event.Nonce = &value
		}
		if latency.Valid {
			value := float64(latency.Int64) / 1000
			event.LatencyMS = &value
		}
		if filter.IncludeResponse && response.Valid && json.Valid([]byte(response.String)) {
			event.Response = json.RawMessage(response.String)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trade audit: %w", err)
	}
	return result, nil
}

func (s *Store) recoverPending(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE trade_events
		SET completed_at_ms = ?, status = 'unknown',
			error = 'server stopped before the mutation outcome was recorded; do not retry blindly'
		WHERE status = 'pending'`, time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("recover pending trade audits: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS mutation_fences (
			venue_key TEXT PRIMARY KEY,
			generation INTEGER NOT NULL CHECK (generation > 0),
			expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms > 0),
			owner_generation INTEGER CHECK (owner_generation > 0),
			updated_at_ms INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create SQLite mutation fence schema: %w", err)
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "expires_at_ms", definition: "INTEGER"},
		{name: "owner_generation", definition: "INTEGER"},
	} {
		if err := s.addMutationFenceColumnIfMissing(ctx, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS mutation_fence_reservations (
			venue_key TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK (generation > 0),
			expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms > 0),
			owner_generation INTEGER CHECK (owner_generation > 0),
			reserved_at_ms INTEGER NOT NULL,
			PRIMARY KEY (venue_key, generation)
		)`); err != nil {
		return fmt.Errorf("create SQLite mutation fence reservation audit schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS trade_events (
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
			venue_key TEXT,
			nonce INTEGER,
			operation_identifiers_json TEXT,
			request_json TEXT,
			venue_request_json TEXT,
			status TEXT NOT NULL,
			error TEXT,
			latency_us INTEGER,
			response_json TEXT
		)`); err != nil {
		return fmt.Errorf("create SQLite audit schema: %w", err)
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "venue_key", definition: "TEXT"},
		{name: "operation_identifiers_json", definition: "TEXT"},
		{name: "request_json", definition: "TEXT"},
		{name: "venue_request_json", definition: "TEXT"},
	} {
		if err := s.addColumnIfMissing(ctx, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS trade_events_symbol_id ON trade_events(symbol, id DESC);
		CREATE INDEX IF NOT EXISTS trade_events_action_id ON trade_events(action, id DESC);
		CREATE INDEX IF NOT EXISTS trade_events_status_id ON trade_events(status, id DESC);
	`); err != nil {
		return fmt.Errorf("create SQLite audit indexes: %w", err)
	}
	return nil
}

func (s *Store) addMutationFenceColumnIfMissing(ctx context.Context, name, definition string) error {
	return s.addColumnIfMissingForTable(ctx, "mutation_fences", name, definition)
}

func (s *Store) addColumnIfMissing(ctx context.Context, name, definition string) error {
	return s.addColumnIfMissingForTable(ctx, "trade_events", name, definition)
}

func (s *Store) addColumnIfMissingForTable(
	ctx context.Context, table, name, definition string,
) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info('"+table+"')")
	if err != nil {
		return fmt.Errorf("inspect SQLite audit schema: %w", err)
	}
	found := false
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&sequence, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan SQLite audit schema: %w", err)
		}
		if columnName == name {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate SQLite audit schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close SQLite audit schema rows: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+name+" "+definition); err != nil {
		return fmt.Errorf("add SQLite %s column %s: %w", table, name, err)
	}
	return nil
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func numberText(value *float64) any {
	if value == nil {
		return nil
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func parseNumber(value sql.NullString) *float64 {
	if !value.Valid {
		return nil
	}
	parsed, err := strconv.ParseFloat(value.String, 64)
	if err != nil {
		return nil
	}
	return &parsed
}
