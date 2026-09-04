# Security

## Report a vulnerability

Open a private GitHub security advisory. Do not put credentials, wallet addresses, order data, or exploit details in a public issue.

## Operator guidance

- Use a restricted Hyperliquid API wallet.
- Keep `HL_PRIVATE_KEY` and `MCP_AUTH_TOKEN` in a secret manager.
- Rotate both credentials after suspected exposure.
- Keep `MAX_NOTIONAL_USD` below the largest intended order.
- Back up the SQLite volume if the audit is important.
- Treat an `unknown` audit result as possibly executed.

This server intentionally has no fund-transfer or withdrawal tools.
