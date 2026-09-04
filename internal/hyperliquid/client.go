package hyperliquid

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxResponseBytes = 8 << 20

type Options struct {
	BaseURL       string
	WalletAddress string
	PrivateKey    string
	DEXes         []string
	MaxNotional   float64
	Timeout       time.Duration
}

// AccountIdentity proves which configured public account this signer can mutate.
type AccountIdentity struct {
	Address                  string `json:"address"`
	Mainnet                  bool   `json:"mainnet"`
	SignerAddress            string `json:"signerAddress"`
	SignerRole               string `json:"signerRole"`
	AuthorizedAccountAddress string `json:"authorizedAccountAddress,omitempty"`
	AssociationVerified      bool   `json:"associationVerified"`
}

type userRoleResponse struct {
	Role string `json:"role"`
	Data *struct {
		User string `json:"user"`
	} `json:"data,omitempty"`
}

type Client struct {
	baseURL        string
	walletAddress  string
	dexes          []string
	maxNotional    float64
	requestTimeout time.Duration
	mainnet        bool
	http           *http.Client
	signer         *Signer
	nonce          atomic.Uint64

	marketMu sync.RWMutex
	markets  *marketCatalog

	accountMu   sync.RWMutex
	accountMode string
}

func NewClient(options Options) (*Client, error) {
	if options.Timeout <= 0 {
		return nil, errors.New("HTTP timeout must be positive")
	}
	if !finitePositive(options.MaxNotional) {
		return nil, errors.New("max notional must be finite and positive")
	}
	signer, err := NewSigner(options.PrivateKey)
	if err != nil {
		return nil, err
	}
	walletAddress, err := parseAddress(options.WalletAddress)
	if err != nil {
		return nil, fmt.Errorf("wallet address: %w", err)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &Client{
		baseURL:        strings.TrimRight(options.BaseURL, "/"),
		walletAddress:  fmt.Sprintf("0x%x", walletAddress),
		dexes:          append([]string(nil), options.DEXes...),
		maxNotional:    options.MaxNotional,
		requestTimeout: options.Timeout,
		mainnet:        !strings.Contains(strings.ToLower(options.BaseURL), "testnet"),
		http: &http.Client{
			Transport: transport,
			Timeout:   options.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		signer: signer,
	}, nil
}

func (c *Client) Initialize(ctx context.Context) error {
	errorsByTask := make(chan error, 3)
	go func() { errorsByTask <- c.RefreshMarkets(ctx) }()
	go func() { errorsByTask <- c.RefreshAccountMode(ctx) }()
	go func() { _, err := c.AccountIdentity(ctx); errorsByTask <- err }()
	for range 3 {
		if err := <-errorsByTask; err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) Close() {
	c.http.CloseIdleConnections()
}

// AccountIdentity verifies the local signer against Hyperliquid's current user-role mapping.
func (c *Client) AccountIdentity(ctx context.Context) (AccountIdentity, error) {
	signerAddress := strings.ToLower(c.signer.Address())
	identity := AccountIdentity{
		Address: c.walletAddress, Mainnet: c.mainnet, SignerAddress: signerAddress,
	}
	var role userRoleResponse
	if err := c.info(ctx, map[string]any{"type": "userRole", "user": signerAddress}, &role); err != nil {
		return identity, fmt.Errorf("verify signer role: %w", err)
	}
	identity.SignerRole = role.Role
	switch role.Role {
	case "user":
		identity.AuthorizedAccountAddress = signerAddress
	case "agent":
		if role.Data == nil {
			return identity, errors.New("agent role omitted its authorized user")
		}
		user, err := parseAddress(role.Data.User)
		if err != nil {
			return identity, fmt.Errorf("authorized user: %w", err)
		}
		identity.AuthorizedAccountAddress = fmt.Sprintf("0x%x", user)
	default:
		return identity, fmt.Errorf("signer role %q cannot authorize this trading account", role.Role)
	}
	identity.AssociationVerified = identity.AuthorizedAccountAddress == c.walletAddress
	if !identity.AssociationVerified {
		return identity, fmt.Errorf(
			"signer authorizes %s, not configured account %s",
			identity.AuthorizedAccountAddress, c.walletAddress,
		)
	}
	return identity, nil
}

func (c *Client) RequestTimeout() time.Duration { return 2 * c.requestTimeout }

func (c *Client) RefreshAccountMode(ctx context.Context) error {
	var mode string
	if err := c.info(ctx, map[string]any{
		"type": "userAbstraction",
		"user": c.walletAddress,
	}, &mode); err != nil {
		return fmt.Errorf("load account abstraction mode: %w", err)
	}
	c.accountMu.Lock()
	c.accountMode = mode
	c.accountMu.Unlock()
	return nil
}

func (c *Client) info(ctx context.Context, request any, response any) error {
	raw, err := c.post(ctx, "/info", request)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return fmt.Errorf("decode Hyperliquid info response: %w", err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, payload any) (json.RawMessage, error) {
	response, _, err := c.postChecked(ctx, path, payload, nil)
	return response, err
}

func (c *Client) postChecked(
	ctx context.Context, path string, payload any,
	beforeNetworkSend func() (release func(), err error),
) (json.RawMessage, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("encode Hyperliquid request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("build Hyperliquid request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "hl-mcp/1.0")
	var release func()
	if beforeNetworkSend != nil {
		release, err = beforeNetworkSend()
		if err != nil {
			return nil, false, err
		}
	}

	response, err := c.http.Do(request)
	if release != nil {
		release()
	}
	if err != nil {
		return nil, true, fmt.Errorf("hyperliquid request failed: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, true, fmt.Errorf("read Hyperliquid response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return nil, true, errors.New("hyperliquid response exceeded 8 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 512 {
			message = message[:512]
		}
		return nil, true, fmt.Errorf("hyperliquid returned HTTP %d: %s", response.StatusCode, message)
	}
	return responseBody, true, nil
}

// ReserveNonce returns a unique nonce for one mutation attempt.
func (c *Client) ReserveNonce() uint64 {
	now := uint64(time.Now().UnixMilli())
	for {
		previous := c.nonce.Load()
		next := now
		if next <= previous {
			next = previous + 1
		}
		if c.nonce.CompareAndSwap(previous, next) {
			return next
		}
	}
}
