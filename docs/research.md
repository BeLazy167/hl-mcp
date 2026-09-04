# Protocol research and implementation decision

Checked on 2026-09-03.

## Decision

This implementation is the best fit for this deployment's narrow requirements: one private Hyperliquid account, low request overhead, direct API access, static bearer authentication, HIP-3 support, and no fund-transfer surface.

It is not a claim that one MCP server is best for every Hyperliquid user. Broader servers expose more tools. That larger surface and dependency graph are drawbacks here.

## MCP version

The current protocol revision is `2026-07-28`. It removes protocol sessions for current clients and requires stateless Streamable HTTP. The official Go SDK supports it when `StreamableHTTPOptions.Stateless` is true.

The Go SDK did not adopt a v2 module path. Its beta was `v1.7.0-pre.1`; stable `v1.7.0` superseded that beta and supports `2026-07-28`. This project pins stable `v1.7.0` rather than the obsolete prerelease.

Sources:

- [MCP 2026-07-28 release](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
- [MCP SDK beta announcement](https://blog.modelcontextprotocol.io/posts/sdk-betas-2026-07-28/)
- [Official Go SDK compatibility table](https://github.com/modelcontextprotocol/go-sdk#version-compatibility)
- [Go SDK v1.7.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.7.0)
- [Stateless HTTP implementation](https://github.com/modelcontextprotocol/go-sdk/blob/v1.7.0/mcp/streamable.go)

Context7 resolved `/modelcontextprotocol/go-sdk` as the high-reputation official SDK. Its Streamable HTTP and typed-tool documentation matched the tagged source.

## Hyperliquid wire behavior

The implementation follows these official behaviors:

- `/info` serves market and account reads.
- `/exchange` accepts signed trading actions.
- Perp asset IDs use the main universe index. Builder DEX assets start at `110000`, with `10000` reserved per DEX.
- L1 actions hash MessagePack action bytes, an eight-byte nonce, and an optional vault address.
- API-wallet identity is recovered from the signature. A different funded account address does not become `vaultAddress`.
- This server does not support vault trading, so it signs and submits every trading action with `vaultAddress: null`.
- The action hash is signed as the EIP-712 `Agent` type on domain `Exchange`, chain ID `1337`.
- Market orders are IOC limit orders around a fresh midpoint. The official SDK default slippage is 5%.
- Attached take-profit and stop-loss orders use one `normalTpsl` order action.
- `clearinghouseState` and `frontendOpenOrders` accept a `dex` field. Reads must fan out per DEX.

Sources:

- [Exchange endpoint](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/exchange-endpoint)
- [Info endpoint](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint)
- [Signing guidance](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/signing)
- [Official Python signing code](https://github.com/hyperliquid-dex/hyperliquid-python-sdk/blob/master/hyperliquid/utils/signing.py)
- [Official Python market-order code](https://github.com/hyperliquid-dex/hyperliquid-python-sdk/blob/master/hyperliquid/exchange.py)
- [Official Python TP/SL example](https://github.com/hyperliquid-dex/hyperliquid-python-sdk/blob/master/examples/basic_tpsl.py)
- [Official Rust signing tests](https://github.com/hyperliquid-dex/hyperliquid-rust-sdk/blob/master/src/exchange/exchange_client.rs)

The test suite copies no SDK implementation. It uses official action hashes and signatures as fixed compatibility vectors.

## Existing MCP alternatives

GitHub search found several public Hyperliquid MCP servers. The most-used results were Python/FastMCP projects backed by `hyperliquid-python-sdk`. Two Go results differed from this project's constraints:

- [`ThisNewMark/hyperliquid-pp-cli`](https://github.com/ThisNewMark/hyperliquid-pp-cli) is a broad CLI and MCP. It used `mark3labs/mcp-go`, `go-ethereum`, MessagePack, browser automation, and transfer/withdraw actions when checked.
- [`duongnv129/hyperliquid-mcp`](https://github.com/duongnv129/hyperliquid-mcp) used the third-party `sonirico/go-hyperliquid` exchange library and official MCP Go SDK `v1.6.1` when checked.

This project instead uses:

- the latest official MCP Go SDK;
- direct Hyperliquid HTTP calls;
- a small secp256k1 dependency;
- a purpose-built MessagePack encoder for the three supported action types;
- no generic exchange or Hyperliquid client library;
- no transfer or withdrawal action;
- SQLite mutation auditing;
- explicit main/XYZ behavior tests for the four migration regressions.

## Research paths

Primary-source discovery used agent-reach through GitHub CLI and web retrieval. Monid Exa found the official Hyperliquid API pages. Monid TinyFish fetched the MCP release notes and Go SDK compatibility text. Context7 supplied current Go SDK API examples. Claims above were then checked against tagged or commit-pinned source.
