package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL      = "https://api.hyperliquid.xyz"
	defaultDBPath      = "data/hl-mcp.db"
	defaultMaxNotional = 3000.0
	defaultPort        = 3000
	defaultHTTPTimeout = 8 * time.Second
)

type Config struct {
	WalletAddress string
	PrivateKey    string
	AuthToken     string
	APIURL        string
	DEXes         []string
	DBPath        string
	MaxNotional   float64
	Port          int
	HTTPTimeout   time.Duration
}

var placeholderSecretMarkers = []string{
	"replace-with", "replace_with", "replace with",
	"generate_at_least", "generate-at-least", "generate at least",
	"your-restricted", "your_restricted",
	"placeholder", "changeme", "example",
}

func isPlaceholderSecret(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range placeholderSecretMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func Load() (Config, error) {
	cfg := Config{
		WalletAddress: strings.TrimSpace(os.Getenv("HL_WALLET_ADDRESS")),
		PrivateKey:    strings.TrimSpace(os.Getenv("HL_PRIVATE_KEY")),
		AuthToken:     strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN")),
		APIURL:        envOr("HL_API_URL", defaultAPIURL),
		DEXes:         parseDEXes(envOr("HL_DEXES", "xyz")),
		DBPath:        envOr("DB_PATH", defaultDBPath),
		MaxNotional:   defaultMaxNotional,
		Port:          defaultPort,
		HTTPTimeout:   defaultHTTPTimeout,
	}

	var missing []string
	for _, item := range []struct {
		name  string
		value string
	}{
		{"HL_WALLET_ADDRESS", cfg.WalletAddress},
		{"HL_PRIVATE_KEY", cfg.PrivateKey},
		{"MCP_AUTH_TOKEN", cfg.AuthToken},
	} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) != 0 {
		return Config{}, fmt.Errorf("required environment variables missing: %s", strings.Join(missing, ", "))
	}
	if len(cfg.AuthToken) < 32 {
		return Config{}, errors.New("MCP_AUTH_TOKEN must contain at least 32 characters")
	}
	if isPlaceholderSecret(cfg.AuthToken) {
		return Config{}, errors.New("MCP_AUTH_TOKEN must be a generated secret; shipped example values are rejected")
	}
	if isPlaceholderSecret(cfg.PrivateKey) {
		return Config{}, errors.New("HL_PRIVATE_KEY must be a generated secret; shipped example values are rejected")
	}
	if !isAddress(cfg.WalletAddress) {
		return Config{}, errors.New("HL_WALLET_ADDRESS must be a 20-byte hexadecimal address")
	}
	if strings.EqualFold(cfg.WalletAddress, "0x0000000000000000000000000000000000000000") {
		return Config{}, errors.New("HL_WALLET_ADDRESS must be the funded account, not the shipped example")
	}

	u, err := url.Parse(cfg.APIURL)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Config{}, errors.New("HL_API_URL must be an absolute HTTP(S) URL")
	}
	if u.Scheme != "https" && (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return Config{}, errors.New("HL_API_URL must be https, or http only for a numeric loopback endpoint")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.RawFragment != "" {
		return Config{}, errors.New("HL_API_URL must not contain credentials, a query, or a fragment")
	}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")

	if raw := strings.TrimSpace(os.Getenv("MAX_NOTIONAL_USD")); raw != "" {
		cfg.MaxNotional, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse MAX_NOTIONAL_USD: %w", err)
		}
	}
	if !isFinitePositive(cfg.MaxNotional) {
		return Config{}, errors.New("MAX_NOTIONAL_USD must be finite and positive")
	}

	if raw := strings.TrimSpace(os.Getenv("PORT")); raw != "" {
		cfg.Port, err = strconv.Atoi(raw)
		if err != nil || cfg.Port < 1 || cfg.Port > 65535 {
			return Config{}, errors.New("PORT must be an integer from 1 through 65535")
		}
	}

	if raw := strings.TrimSpace(os.Getenv("HL_HTTP_TIMEOUT")); raw != "" {
		cfg.HTTPTimeout, err = time.ParseDuration(raw)
		if err != nil || cfg.HTTPTimeout <= 0 {
			return Config{}, errors.New("HL_HTTP_TIMEOUT must be a positive Go duration")
		}
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func parseDEXes(raw string) []string {
	seen := make(map[string]struct{})
	var result []string
	for part := range strings.SplitSeq(raw, ",") {
		dex := strings.ToLower(strings.TrimSpace(part))
		if dex == "" || dex == "main" {
			continue
		}
		if _, ok := seen[dex]; ok {
			continue
		}
		seen[dex] = struct{}{}
		result = append(result, dex)
	}
	return result
}

func isAddress(value string) bool {
	value = strings.TrimPrefix(value, "0x")
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return false
		}
	}
	return true
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}
