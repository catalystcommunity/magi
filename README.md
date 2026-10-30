# Magi

Magi is a composable automation and multi-agent system for personal,
household, and business work. It is licensed under the
[Apache License, Version 2.0](LICENSE).

Magi is a set of separate primitives with their own APIs. The `magi/`
composition layer uses them to give people one conversation for questions,
knowledge, and work. Clients (command line, web, Android) use the API.

- [Plan](docs/plan.md): goal, primitives, and slices.
- [Conventions](docs/conventions.md): layout, rules, transport, storage, tests.
- Each primitive has a README in `primitives/<name>/`.

## Health endpoints

Every service answers `GET /healthz` with `ok` and no token. A service that
depends on storage or engines answers 503 until that dependency answers, so
the endpoint doubles as the Kubernetes readiness probe. The paths below are
relative to the service's listen address (`MAGI_LISTEN`, default `:8080`).

| Service | Endpoint | Checks |
| --- | --- | --- |
| `magi` | `GET /healthz` | The API and web server process. |
| `artifacts` | `GET /healthz` | The process and the PostgreSQL database. |
| `conversation` | `GET /healthz` | The process and the PostgreSQL database. |
| `knowledge` | `GET /healthz` | The process and the PostgreSQL database. |
| `workflow` | `GET /healthz` | The process and the PostgreSQL database. |
| `execution` | `GET /healthz` | The process and the PostgreSQL database. |
| `identity` | `GET /healthz` | The process and the PostgreSQL database. |
| `source` | `GET /healthz` | The process and the repository directory. |
| `speech` | `GET /healthz` | The process and both speech engines. |
| `release` | `GET /healthz` | The controller process. |
| `inference` | `GET /healthz` | The host process. |
| `envd` (per environment) | `GET /healthz` | The environment daemon. No token. |
| Speech engine (STT/TTS) | `GET /health` | The engine process, device, and load. |
| Inference engine | `GET /health` | The model engine behind the host. |

The agent worker claims workflow tasks and serves no HTTP endpoint.

## Try it

To install Magi, read deploy/README.md. To use the command-line client, read magi/README.md.
