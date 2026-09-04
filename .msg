Add a server-enforced read-only bearer

- MCP_READONLY_AUTH_TOKEN routes to a second MCP server that registers read tools only
- Middleware rejects any non-read tools/call on that server before dispatch
- Startup rejects short, placeholder, or writer-equal read-only tokens
- Constant-time bearer routing; writer behaviour and fencing unchanged
