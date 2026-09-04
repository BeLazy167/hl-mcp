# Hyperliquid API coverage

Checked against the official Hyperliquid documentation on 2026-09-03.

## Conclusion

The original ten MCP tools cover the previous server's behavior. This release adds 14 read tools without expanding custody risk, for 24 tools total. A later release can add six trading controls after the Go build passes a live canary. Transfers, withdrawals, account authority changes, vault operations, and deployment actions should remain unavailable.

## Rules that every tool must follow

### Account and signer addresses are different concepts

An API wallet signs for a funded master account. This server does not support vault or subaccount routing. Read requests use the funded account address. Signed actions recover the API-wallet address from the signature. Hyperliquid calls API wallets "agent wallets."

The MCP must not infer `vaultAddress` from an address mismatch. This deployment does not support vault trading, so every signed action uses `vaultAddress: null`.

Source: [Nonces and API wallets](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/nonces-and-api-wallets)

### Nonces belong to the signer

Hyperliquid stores the 100 highest nonces for each signer. A nonce must be unique and fall within two days before or one day after the block timestamp. One API wallet shared by several processes also shares one nonce set.

The current single-Machine design and atomic millisecond counter fit this model. New mutation tools must use the same `submit` path. They must not retry an ambiguous request.

Source: [Nonces and API wallets](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/nonces-and-api-wallets#hyperliquid-nonces)

### Order creation uses evidence fences

`hl_set_leverage` and `hl_place_order` require positive integer `fenceGeneration` and future Unix-millisecond `fenceExpiresAtMs` fields. SQLite atomically stores the highest generation per Hyperliquid network and funded account. Lower generations fail. The current generation can set leverage and then place an order while each request is unexpired.

The server serializes these handlers through send. It signs and sends `fenceExpiresAtMs` as Hyperliquid `expiresAfter`, with another expiry check immediately before network I/O. Cancel tools do not require a fence, so risk-reducing cancellation stays available.

### Asset IDs depend on the market class

Main perpetuals use their index in `meta`. Builder-deployed perpetuals use `100000 + dex_index * 10000 + market_index`. Spot pairs use `10000 + spot_index`.

MCP tools should continue accepting normalized symbols. The market catalog should convert those symbols to Hyperliquid coin names and asset IDs.

Source: [Asset IDs](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/asset-ids)

### Price and size formatting is part of signing

A price accepts at most five significant figures and at most `6 - szDecimals` decimal places for perpetuals. Sizes accept at most `szDecimals` decimal places. Integer prices remain valid. Encoded numbers must omit trailing zeroes.

Source: [Tick and lot size](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/tick-and-lot-size)

### History calls need bounded pagination

Time-range responses contain at most 500 items or distinct blocks. A caller continues from the last returned timestamp. `userFillsByTime` exposes only the latest 10,000 fills and returns at most 2,000 fills per call. `candleSnapshot` exposes only the latest 5,000 candles.

Each MCP call should return one bounded page. It must not loop until it exhausts account history.

Source: [Info endpoint](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint)

### Rate limits favor focused calls

REST requests share an IP limit of 1,200 weight per minute. Exchange requests usually cost `1 + floor(batch_length / 40)`. `l2Book`, `allMids`, `clearinghouseState`, `orderStatus`, and `spotClearinghouseState` cost 2. Most other info requests cost 20. Large history and candle responses add weight based on returned item count.

Address limits apply only to actions. Each address starts with a 10,000-request buffer and then earns one request per USDC traded. Cancel capacity is larger than ordinary action capacity.

Source: [Rate limits and user limits](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/rate-limits-and-user-limits)

## Original MCP coverage

| MCP tool | Hyperliquid request or action |
|---|---|
| `hl_balance` | `spotClearinghouseState` or `clearinghouseState` |
| `hl_positions` | `clearinghouseState` for each configured perpetual DEX |
| `hl_ticker` | cached `metaAndAssetCtxs` plus `allMids` |
| `hl_search_markets` | cached `metaAndAssetCtxs` |
| `hl_open_orders` | `frontendOpenOrders` for each configured perpetual DEX |
| `hl_set_leverage` | `updateLeverage` |
| `hl_place_order` | `order` |
| `hl_cancel_order` | `cancel` |
| `hl_cancel_all` | `frontendOpenOrders`, then `cancel` for one symbol |
| `hl_get_trades` | local SQLite audit only |

## Implemented read expansion

These tools do not sign actions. Account identity calls `/info` `userRole` and exposes only public addresses; the other account reads also call `/info`.

| MCP tool | Request type | Inputs and documented limits |
|---|---|---|
| `hl_account_identity` | `userRole` | No inputs. Returns the normalized funded account, public signer, signer role, authorized account, verified association, and network. |
| `hl_order_book` | `l2Book` | `symbol`; optional `nSigFigs` in `2,3,4,5`; optional `mantissa` in `1,2,5` only when `nSigFigs=5`. Returns at most 20 levels per side. |
| `hl_candles` | `candleSnapshot` | `symbol`, `interval`, `startTime`, `endTime`. Intervals are `1m,3m,5m,15m,30m,1h,2h,4h,8h,12h,1d,3d,1w,1M`. |
| `hl_user_fills` | `userFills` or `userFillsByTime` | Optional `startTime`, `endTime`, and `aggregateByTime`. A supplied `startTime` selects the time-range request. |
| `hl_order_history` | `historicalOrders` | Optional local `limit` no higher than 2,000. Hyperliquid returns the latest 2,000 orders. |
| `hl_order_status` | `orderStatus` | `id`, as a decimal order ID or a 16-byte hexadecimal client order ID. |
| `hl_funding_history` | `fundingHistory` | `symbol`, `startTime`, and optional `endTime`. HIP-3 uses the DEX-prefixed coin name. |
| `hl_user_funding` | `userFunding` | `startTime` and optional `endTime`. This returns the configured account's funding payments. |
| `hl_predicted_funding` | `predictedFundings` | Optional local symbol filter. Hyperliquid supports only the first perpetual DEX. |
| `hl_portfolio` | `portfolio` | No account input. The server always uses the configured account. |
| `hl_fees` | `userFees` | No account input. |
| `hl_rate_limit` | `userRateLimit` | No account input. |
| `hl_spot_balances` | `spotClearinghouseState` | No account input. This is the balance source of truth for unified and portfolio-margin accounts. |
| `hl_active_asset_data` | `activeAssetData` | `symbol`. Returns current leverage, mark price, available size, and maximum trade sizes. |

Sources:

- [General info requests](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint)
- [Perpetual info requests](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint/perpetuals)
- [Spot info requests](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint/spot)

### HIP-3 behavior

`l2Book`, `candleSnapshot`, `fundingHistory`, and `activeAssetData` take a coin name. Builder-deployed markets require the `{dex}:{coin}` form. The MCP should map `XYZ-GOLD/USDC:USDC` to `xyz:GOLD` through the existing catalog.

`predictedFundings` does not support builder-deployed perpetual DEXes. `userFills`, `historicalOrders`, and `orderStatus` do not accept a DEX input.

### Read endpoints to defer

The docs also expose public metadata, staking history, vault data, referrals, subaccounts, account roles, abstraction state, and borrow/lend state. Most do not help this trading MCP. Staking, vault, delegation, and account-authority tools also conflict with the deployment's narrow permission model.

The rate-limit page names `recentTrades`, but the HTTP info documentation does not define its request and response schema. Public trades are fully documented as a WebSocket subscription. Do not add an HTTP `recentTrades` tool until Hyperliquid documents that contract.

## Add these trading controls after a canary

All new mutations must verify the current signer-to-funded-account association, then write `pending` to SQLite before `/exchange`. Reverify the association immediately before network send. The pending row must contain the reserved nonce, public account venue key, safe request identity, and any provided idempotency identifiers. The unsigned venue action must be durably added before network send. Never store private keys, signatures, or bearer tokens. Mutations must record `succeeded`, `failed`, `partial`, or `unknown` using the existing rules.

| Proposed tool | Action | Required behavior |
|---|---|---|
| `hl_modify_order` | `modify` | Replace one order by order ID or client order ID. Require the complete replacement order. Omit `a` when `always_place` is false. Default `always_place` to false. |
| `hl_close_position` | `order` | Read the current position, then submit an opposite IOC reduce-only order. Abort if the position is already flat. Record the read-submit race in the tool description. |
| `hl_place_trigger_order` | `order` | Submit one reduce-only `trigger` order with `tpsl` equal to `tp` or `sl`. Use `grouping: "na"` for a standalone trigger. |
| `hl_cancel_by_cloid` | `cancelByCloid` | Require a symbol and a 16-byte hexadecimal client order ID. Resolve the asset through the market catalog. |
| `hl_cancel_all_orders` | `cancel` | Read every configured DEX, build one bounded batch, and cancel every resting and trigger order. Keep the current symbol-scoped `hl_cancel_all`. |
| `hl_schedule_cancel` | `scheduleCancel` | Accept either a future timestamp or an explicit disable operation. A deadline must be at least five seconds ahead. Hyperliquid permits ten triggered deadlines per UTC day. |

Source: [Exchange endpoint](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/exchange-endpoint)

### Improve the existing order and cancel tools

`hl_place_order` accepts `parentClientOrderId`, `takeProfitClientOrderId`, and `stopLossClientOrderId`. Each is exactly `0x` plus 32 hexadecimal characters. Attached legs require their matching client order IDs, and stray child IDs are rejected.

It also accepts optional `takeProfitLimitPrice` and `stopLossLimitPrice` fields. Each child limit requires its matching trigger, must be finite and positive, and is formatted with Hyperliquid's price rules before signing. For a parent buy, attached exits sell, so each formatted child limit must be at or below its formatted trigger. For a parent sell, attached exits buy, so each limit must be at or above its trigger. Explicit child limits are signed with the parent and triggers in the same `normalTpsl` action and returned as `limitPrice`. When a child limit is omitted, the server derives and returns a 5% execution bound from the formatted trigger.

Cancel results are accepted only when the response action type is `cancel` and the response contains exactly one status per submitted cancellation. Any wrong type, wrong count, or malformed status is an `unknown` outcome and must not be retried blindly.

It can later accept:

- `timeInForce`, with `Gtc`, `Ioc`, or `Alo`;
- a standalone trigger mode through the separate tool above.

Hyperliquid recommends the `fast` flag for ordinary cancels. Fast cancels cannot cancel trigger orders. The MCP must not enable it blindly on `hl_cancel_all` because that list can contain attached TP and SL orders.

Source: [Optimizing latency](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/optimizing-latency)

### Defer these trading actions

`twapOrder` and `twapCancel` are useful but add a second order lifecycle and separate status shapes. Add them only after ordinary order, trigger, modify, and cancel paths pass live verification.

`updateIsolatedMargin` and `topUpIsolatedOnlyMargin` change collateral allocation. They are not fund withdrawals, but an incorrect call can change liquidation risk. Keep them out until there is a specific use case.

## Do not expose these actions

The exchange endpoint supports many actions that do not belong in this MCP:

- `sendAsset`, `agentSendAsset`, `usdSend`, `spotSend`, `usdClassTransfer`, and `withdraw3`;
- staking deposits, staking withdrawals, and `tokenDelegate`;
- `vaultTransfer` and HIP-3 backstop transfers;
- `approveAgent`, builder-fee approval, and account abstraction changes;
- subaccount creation and subaccount transfers;
- token, perpetual, validator, oracle, and outcome deployment actions;
- reward claims and administrative actions.

These actions move funds, grant authority, or manage protocol infrastructure. A leaked MCP bearer token must never gain those powers.

## WebSocket findings

Hyperliquid documents subscriptions for `allMids`, `l2Book`, `trades`, `bbo`, candles, asset contexts, clearinghouse state, open orders, order updates, fills, funding, ledger updates, TWAP state, and user events.

WebSocket post requests carry the same info and action payloads as HTTP. They do not add new request semantics. Connections close after 60 seconds without a received message. A client can send `{"method":"ping"}` and receives `{"channel":"pong"}`.

A stateless MCP call cannot represent an unbounded subscription cleanly. Keep snapshot tools on HTTP. Add a shared WebSocket cache only when a caller needs live BBO, public trades, or order-update latency.

Sources:

- [WebSocket subscriptions](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/websocket/subscriptions)
- [WebSocket post requests](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/websocket/post-requests)
- [WebSocket timeouts and heartbeats](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/websocket/timeouts-and-heartbeats)

## Error handling requirements

Order and cancel responses usually contain one status for each batched item. Pre-validation can instead return one error for the whole batch. New batch tools must handle both forms.

Documented order failures include invalid ticks, a notional below $10, insufficient margin, reduce-only violations, invalid ALO prices, unmatched IOC orders, bad trigger prices, unavailable market liquidity, open-interest limits, and oracle limits. A cancel can return `MissingOrder`.

An HTTP success does not prove that every action succeeded. Every mutation handler must inspect each returned status before marking the audit row successful.

Source: [Error responses](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/error-responses)

## Recommended release order

1. Keep the current deployed code until its agent-wallet order path passes an explicitly authorized live canary.
2. Deploy the implemented 13-tool read expansion.
3. Add the six trading controls with fixed signing vectors, fake-exchange tests, and SQLite audit tests.
4. Run a second low-notional canary for standalone triggers, modification, and position closing.
5. Consider WebSocket caching only after measured HTTP behavior shows a real need.

The read release exposes 24 tools in total. The trading-control release would expose 30. This is large but still focused on market data, account state, and trading. It excludes custody and protocol administration.
