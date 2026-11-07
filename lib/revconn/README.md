# revconn

Lets a machine that only connects out serve HTTP to a server.

- The agent (`Agent`) keeps `Idle` connections open to the server. It
  opens each one with an HTTP/1.1 upgrade (the WebSocket handshake, so
  that gateways pass it) and a bearer token. Then it serves HTTP on the
  connection.
- The server (`Pool.Handler`) checks the token, keeps the idle
  connections of each agent, and closes an idle connection after
  `MaxAge` (4 minutes). It sees at once when the agent closes one.
- `Pool.Transport` sends a request for `http://<agent>.revconn/...` on an
  idle connection of the agent, and other requests to the next transport.
  Each connection carries one request. The connection to the server is
  already TLS, so the URL uses `http`.
- When the agent opened no connection in the last minute, a request fails
  at once with `ErrNoAgent`. Else it waits up to `Wait` (2 s) for an idle
  connection.

Used by `primitives/inference` (an inference host that connects out) and
`magi/cmd/magi-server` (`/inference/connect`).

Tests: `go test -race ./...`.
