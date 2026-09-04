package config

import (
	"strings"
	"testing"
)

const (
	testWriterToken   = "01234567890123456789012345678901"
	testReadOnlyToken = "11234567890123456789012345678901"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HL_WALLET_ADDRESS", "0x1111111111111111111111111111111111111111")
	t.Setenv("HL_PRIVATE_KEY", "0123456789012345678901234567890123456789012345678901234567890123")
	t.Setenv("MCP_AUTH_TOKEN", testWriterToken)
	t.Setenv("MCP_READONLY_AUTH_TOKEN", testReadOnlyToken)
}

func TestLoad(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HL_DEXES", "xyz, XYZ,main")
	t.Setenv("MAX_NOTIONAL_USD", "123.45")
	t.Setenv("PORT", "4000")
	t.Setenv("HL_HTTP_TIMEOUT", "2s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DEXes) != 1 || cfg.DEXes[0] != "xyz" || cfg.MaxNotional != 123.45 || cfg.Port != 4000 {
		t.Fatalf("config values were not loaded")
	}
	if cfg.AuthToken != testWriterToken || cfg.ReadOnlyAuthToken != testReadOnlyToken {
		t.Fatal("auth tokens were not loaded")
	}
}

func TestLoadRejectsShippedExampleSecrets(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MCP_AUTH_TOKEN", "GENERATE_AT_LEAST_32_RANDOM_CHARACTERS")
	if _, err := Load(); err == nil {
		t.Fatal("expected example bearer rejection")
	}
	t.Setenv("MCP_AUTH_TOKEN", testWriterToken)
	t.Setenv("HL_PRIVATE_KEY", "0xYOUR_RESTRICTED_API_WALLET_PRIVATE_KEY")
	if _, err := Load(); err == nil {
		t.Fatal("expected example private key rejection")
	}
	t.Setenv("HL_PRIVATE_KEY", "0123456789012345678901234567890123456789012345678901234567890123")
	t.Setenv("HL_WALLET_ADDRESS", "0x0000000000000000000000000000000000000000")
	if _, err := Load(); err == nil {
		t.Fatal("expected example wallet rejection")
	}
}

func TestLoadRejectsInvalidReadOnlyToken(t *testing.T) {
	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "missing", token: ""},
		{name: "short", token: "short"},
		{name: "shipped placeholder", token: "GENERATE_AT_LEAST_32_DIFFERENT_RANDOM_CHARACTERS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("MCP_READONLY_AUTH_TOKEN", test.token)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "MCP_READONLY_AUTH_TOKEN") {
				t.Fatalf("error = %v, want read-only token rejection", err)
			}
		})
	}
}

func TestLoadRejectsEqualAuthTokens(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MCP_READONLY_AUTH_TOKEN", testWriterToken)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("error = %v, want distinct-token rejection", err)
	}
}

func TestLoadRejectsPlaintextRemoteAPIURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("HL_API_URL", "http://api.hyperliquid.xyz")
	if _, err := Load(); err == nil {
		t.Fatal("expected remote plaintext rejection")
	}
	t.Setenv("HL_API_URL", "http://127.0.0.1:9999")
	if _, err := Load(); err != nil {
		t.Fatalf("numeric loopback must stay allowed: %v", err)
	}
}

func TestLoadRejectsEmbeddedURLComponents(t *testing.T) {
	setRequiredEnv(t)
	for _, apiURL := range []string{
		"https://user:secret@api.hyperliquid.xyz",
		"https://api.hyperliquid.xyz?token=abc",
		"https://api.hyperliquid.xyz#fragment",
	} {
		t.Setenv("HL_API_URL", apiURL)
		if _, err := Load(); err == nil {
			t.Fatalf("HL_API_URL = %q must be rejected", apiURL)
		}
	}
}

func TestLoadRejectsShortToken(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MCP_AUTH_TOKEN", "short")
	if _, err := Load(); err == nil {
		t.Fatal("expected token error")
	}
}
