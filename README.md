# hl-mcp

A small, remote MCP server for Hyperliquid. It is written entirely in Go and calls Hyperliquid's HTTP API directly.

The server exposes account reads, market data, order placement, cancellation, and a local SQLite trade audit. It deliberately exposes no transfer, withdrawal, delegation, vault, or key-management action.

## Why this implementation

- Direct `/info` and `/exchange` calls. No ccxt or exchange abstraction layer.
- Hyperliquid action signing implemented against official Python and Rust SDK vectors.
- MCP protocol `2026-07-28` in stateless mode through the official Go SDK.
- Main-perp and configured HIP-3 DEX reads run concurrently.
- Market orders use a fresh midpoint and a 5% IOC price bound.
- Parent, take-profit, and stop-loss orders use one `normalTpsl` action.
- Attached orders accept explicit `takeProfitLimitPrice` and `stopLossLimitPrice` execution bounds. If omitted, the server derives and reports a 5% bound from the formatted trigger.
- Every trade mutation durably records its nonce and reconciliation identity before contacting `/exchange`.
- One static bearer token protects the remote MCP endpoint.

See [docs/research.md](docs/research.md) for the protocol decision and [docs/api-surface.md](docs/api-surface.md) for the official Hyperliquid API review.

## Tools

| Tool | Purpose |
|---|---|
| `hl_account_identity` | Verify the public signer-to-funded-account mapping and network through `userRole`. |
| `hl_balance` | Read free, used, and total USDC. |
| `hl_positions` | Read nonzero positions from main perps and every configured DEX. |
| `hl_ticker` | Read midpoint and impact bid/ask for one market. |
| `hl_search_markets` | Search configured markets from the in-memory catalog. |
| `hl_open_orders` | Read frontend orders, including trigger orders, per DEX. |
| `hl_order_book` | Read up to 20 bid and ask levels. |
| `hl_candles` | Read bounded OHLCV history. |
| `hl_user_fills` | Read recent or time-bounded account fills. |
| `hl_order_history` | Read historical account orders. |
| `hl_order_status` | Read one order by order ID or client order ID. |
| `hl_funding_history` | Read market funding-rate history. |
| `hl_user_funding` | Read account funding payments. |
| `hl_predicted_funding` | Read main-DEX funding forecasts across venues. |
| `hl_portfolio` | Read portfolio value, PnL, and volume history. |
| `hl_fees` | Read account fee rates and volume. |
| `hl_rate_limit` | Read account action-rate-limit usage. |
| `hl_spot_balances` | Read all spot token balances. |
| `hl_active_asset_data` | Read leverage and account-specific market limits. |
| `hl_mutation_contract` | Attest the enforced mutation contract (read-only). |
| `hl_reserve_fence` | Reserve the next writer-owned mutation generation. Non-idempotent. |
| `hl_set_leverage` | Set cross leverage with the exact current writer-reserved fence. |
| `hl_place_order` | Place fenced limit or market orders with optional attached TP/SL triggers and execution limits. |
| `hl_cancel_order` | Cancel one order. |
| `hl_cancel_all` | Cancel every open order for one symbol. |
| `hl_get_trades` | Read the local mutation audit, newest first. |

Perp symbols use ccxt-compatible names for migration compatibility:

- Main: `BTC/USDC:USDC`
- HIP-3: `XYZ-GOLD/USDC:USDC`

## Configuration

| Variable | Required | Default | Purpose |
|---|---:|---:|---|
| `HL_WALLET_ADDRESS` | yes | — | Funded master account that `userRole` maps the signer to. |
| `HL_PRIVATE_KEY` | yes | — | Trading/API wallet key. |
| `MCP_AUTH_TOKEN` | yes | — | Remote bearer token, at least 32 characters. |
| `MAX_NOTIONAL_USD` | no | `3000` | Ceiling for parent wire notional, except verified reduce-only closes. |
| `HL_DEXES` | no | `xyz` | Comma-separated HIP-3 DEX names. Main perps are always included. |
| `HL_API_URL` | no | `https://api.hyperliquid.xyz` | Hyperliquid API base URL. |
| `HL_HTTP_TIMEOUT` | no | `8s` | Timeout for one Hyperliquid HTTP call. |
| `DB_PATH` | no | `data/hl-mcp.db` | SQLite audit path. The image uses `/data/hl-mcp.db`. |
| `PORT` | no | `3000` | HTTP port. |

Copy `.env.example` for local development. Never use a main-wallet private key when a restricted API wallet will work.

`HL_WALLET_ADDRESS` is the funded master account, so it normally differs from an API wallet signer. Startup and every mutation verify that `userRole` maps the signer to this account. The server sends `vaultAddress: null`; vault and subaccount trading are not supported.

## Run locally

Requires Go 1.25.13 or newer.

```bash
go test ./...
go run ./cmd/hl-mcp
```

Endpoints:

- `GET /healthz` — unauthenticated liveness only
- `POST /mcp` — bearer-authenticated Streamable HTTP

Clients must send:

```http
Authorization: Bearer YOUR_MCP_AUTH_TOKEN
```

The server supports the current stateless MCP protocol and legacy initialization clients from the same endpoint.

## SQLite audit

`hl_get_trades` reads `trade_events`. Each mutation moves through these states:

- `pending`: written before the network mutation starts
- `succeeded`: Hyperliquid accepted the mutation
- `failed`: Hyperliquid definitely rejected it or local validation failed
- `unknown`: the network outcome is ambiguous; do not retry blindly
- `partial`: a parent order may be live while an attached TP/SL failed

The database uses WAL mode and `synchronous=FULL`. Pending rows store the public account venue key, exact nonce, safe request fields, provided CLOIDs, and the unsigned venue action before `/exchange`. The `mutation_fences` table stores the writer-reserved generation and reservation expiry per network/account venue key. Private keys, signatures, and bearer tokens are never stored. Responses are stored locally but omitted from tool output unless `includeResponse` is true.

Run one Fly Machine when using the included single-volume deployment. SQLite is not shared across Machines.

## Deploy to Fly.io

Create the volume once in the app's primary region:

```bash
fly volumes create hl_mcp_data --region ord --size 1
```

Set secrets without writing them into the repository:

```bash
fly secrets set \
  HL_WALLET_ADDRESS=... \
  HL_PRIVATE_KEY=... \
  MCP_AUTH_TOKEN="$(openssl rand -hex 32)" \
  MAX_NOTIONAL_USD=3000
```

Then deploy:

```bash
fly deploy
```

The included `fly.toml` keeps one Machine warm and mounts `hl_mcp_data` at `/data`.

## Safety behavior

- `hl_reserve_fence` atomically allocates the next generation per network/account and returns it with its reservation expiry. The expiry must be in the future and at most five minutes away. The call is non-idempotent: every reservation fences every older generation immediately. `ownerGeneration` is optional audit metadata only.
- `hl_set_leverage` and `hl_place_order` accept only the exact currently reserved generation and its exact reserved expiry. A newer reservation rejects every older pending mutation, in every process sharing the audit database.
- Reservation and the pre-send recheck serialize through a cross-process send lock on `<db>.send.lock`. No newer reservation can interleave between an older request's final fence check and its network send. `fenceExpiresAtMs` is signed and sent as Hyperliquid `expiresAfter`, then rechecked immediately before network I/O under the send lock.
- One reservation may cover leverage followed by placement in one dispatch while unexpired.
- Cancel tools remain available without an evidence fence.
- The audit database owns the generation watermark. If it is lost or restored to an older state, treat every writer bearer as compromised: rotate credentials before resuming. Never restore Eve and this writer independently.
- Shipped example credentials (`GENERATE_AT_LEAST_32_RANDOM_CHARACTERS`, the example private key, the zero address) are rejected at startup.
- `HL_API_URL` must be HTTPS, or plain HTTP only for a numeric loopback endpoint (`127.0.0.1`, `[::1]`).
- `hl_mutation_contract` attests this enforcement so callers can pin the implementation before trusting it with live orders.
- Mutation requests are never retried automatically.
- Nonces remain unique across concurrent calls in one process.
- Unknown symbols, invalid precision, invalid client order IDs, and invalid child-limit directions fail before `/exchange`.
- A child limit must accompany its matching trigger. Sell-exit limits cannot exceed the trigger. Buy-exit limits cannot be below it.
- Cancel responses with an unexpected action type, malformed status, or wrong status count are `unknown`; do not retry blindly.
- `MAX_NOTIONAL_USD` uses the formatted parent wire price, not the midpoint or unrounded input.
- An above-cap reduce-only parent is allowed only when a fresh position read proves the side closes an opposite position and the amount does not exceed it.
- The reduce-only proof is a snapshot, so the position can change before submission. If the position cannot be read or verified, the cap remains active. Hyperliquid still enforces reduce-only at execution.
- A failed initial audit write blocks the mutation.
- A failed final audit update does not hide an accepted order.
- Request bodies are limited to 1 MiB.
- Secrets are read only from environment variables.
- In the Fly image, the process fixes root-owned volume permissions and drops permanently to uid/gid `65532` before opening the database or network.

A bearer-token leak can still place losing trades or cancel protection. Treat the token and API wallet key as secrets.

## License

MIT
