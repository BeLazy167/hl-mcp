package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/BeLazy167/hl-mcp/internal/audit"
	"github.com/BeLazy167/hl-mcp/internal/config"
	"github.com/BeLazy167/hl-mcp/internal/hyperliquid"
	"github.com/BeLazy167/hl-mcp/internal/mcpserver"
	"github.com/BeLazy167/hl-mcp/internal/runtimeuser"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := runtimeuser.PrepareDataDirectory(cfg.DBPath); err != nil {
		return err
	}
	store, err := audit.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	client, err := hyperliquid.NewClient(hyperliquid.Options{
		BaseURL: cfg.APIURL, WalletAddress: cfg.WalletAddress, PrivateKey: cfg.PrivateKey,
		DEXes: cfg.DEXes, MaxNotional: cfg.MaxNotional, Timeout: cfg.HTTPTimeout,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 3*cfg.HTTPTimeout+5*time.Second)
	err = client.Initialize(startupCtx)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("initialize Hyperliquid client: %w", err)
	}

	toolServer := mcpserver.New(client, store)
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return toolServer.MCP() },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          1 << 20,
			PropagateRequestCancellation: true,
		},
	)
	tokenHash := sha256.Sum256([]byte(cfg.AuthToken))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.Handle("/mcp", requireBearer(tokenHash, mcpHandler))

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      2*cfg.HTTPTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go refreshMetadata(ctx, client)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*cfg.HTTPTimeout+5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
		}
	}()

	slog.Info("hl-mcp listening", "port", cfg.Port, "dexes", cfg.DEXes, "maxNotionalUsd", cfg.MaxNotional)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func health(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(map[string]any{"ok": true, "service": "hl-mcp"})
}

func requireBearer(expected [32]byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		header := request.Header.Get("Authorization")
		presented := ""
		if strings.HasPrefix(header, "Bearer ") {
			presented = strings.TrimPrefix(header, "Bearer ")
		}
		presentedHash := sha256.Sum256([]byte(presented))
		if presented == "" || subtle.ConstantTimeCompare(expected[:], presentedHash[:]) != 1 {
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("Cache-Control", "no-store")
			response.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(response).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		response.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(response, request)
	})
}

func refreshMetadata(ctx context.Context, client *hyperliquid.Client) {
	marketTicker := time.NewTicker(15 * time.Minute)
	accountTicker := time.NewTicker(5 * time.Minute)
	defer marketTicker.Stop()
	defer accountTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-marketTicker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, 2*client.RequestTimeout())
			err := client.RefreshMarkets(refreshCtx)
			cancel()
			if err != nil {
				slog.Warn("market metadata refresh failed", "error", err)
			}
		case <-accountTicker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, client.RequestTimeout()/2)
			err := client.RefreshAccountMode(refreshCtx)
			cancel()
			if err != nil {
				slog.Warn("account mode refresh failed", "error", err)
			}
		}
	}
}
