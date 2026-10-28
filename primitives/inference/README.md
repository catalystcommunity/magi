# inference

The inference primitive runs a model engine on a machine that also does
other work, such as a desktop. It starts the engine when a request comes,
and stops it after an idle time. When the machine is busy with other work,
it refuses new work, so that callers use a different provider.

The host is a plain process. It runs in the same way on a host and in a VM.
It does not need Kubernetes.

Contract: `contracts/inference.csil`, service `InferenceService`.

## API

| Request | Result |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions, forwarded to the engine |
| `GET /v1/models` | Forwarded to the engine |
| `InferenceService/status` | State, reason, model, requests in flight |
| `InferenceService/warm` | Starts the engine if the machine is not busy. It does not wait. |
| `GET /healthz` | Health of the host process |

Every request, except `/healthz`, needs a Magi token with the scope
`inference:use`. The host does not send the token to the engine.

A refused chat request has status 503, a `Retry-After` header, and a JSON
error with one of these types:

| Type | Meaning |
| --- | --- |
| `warming` | The engine is starting. Use another provider now, or try again soon. |
| `host_busy` | The machine is busy. The message tells why. |
| `engine_error` | The engine did not start or did not answer. |

States: `off`, `starting`, `ready`, `busy`.

## Busy rules

The host takes a sample every 5 seconds. The machine is busy when one of
these is true:

- A process with a listed name runs. The default list has compilers and
  linkers (`cc1`, `cc1plus`, `rustc`, `compile`, `ld`, `ld.lld`, `mold`,
  `javac`) and game processes (`gamescope`, `reaper`, `wineserver`).
- The 1-minute load average per CPU is more than `cpu_load` (0.6).
- The GPU use is more than `gpu_util` percent (40) while the engine does
  not answer a request.
- The engine does not run, and the free GPU memory is less than `need_mib`
  (7000).

When the machine becomes busy, the host stops the engine as soon as no
request uses it. This gives the GPU back. The machine must then be quiet
for `quiet_for` (60 s) before the host starts the engine again.

GPU values come from `nvidia-smi`. Without it, the GPU rules do not apply.

## Configuration

One JSON file, given with `-config` or `MAGI_INFERENCE_CONFIG`:

```json
{
  "listen": "0.0.0.0:8095",
  "auth_keys": ["/path/to/issuer-public.json"],
  "engine": {
    "command": ["/path/to/llama-server", "-m", "/path/to/model.gguf", "--jinja",
      "--reasoning-budget", "0", "-ngl", "99", "--host", "127.0.0.1", "--port", "18091"],
    "port": 18091,
    "model": "model-name"
  },
  "idle_stop": "15m",
  "start_wait": "0s",
  "engine_log": "/path/to/engine.log",
  "rules": {"cpu_load": 0.6, "gpu_util": 40, "need_mib": 7000, "quiet_for": "60s",
            "processes": ["cc1plus", "rustc", "gamescope"]}
}
```

- `engine.command` must serve the OpenAI API and `GET /health` on
  `127.0.0.1:<port>`. The host starts it in its own process group and
  stops the group.
- `start_wait` is how long a request waits for a cold engine. With `0s`
  (the default), the host refuses at once with `warming`. Use `0s` when the
  caller has another provider.
- `rules` replaces all default rules when it is given.

## Run as a user service

```ini
# ~/.config/systemd/user/magi-inference.service
[Unit]
Description=Magi inference host
After=network-online.target

[Service]
ExecStart=%h/.local/bin/magi-inference -config %h/.config/magi-inference/config.json
Restart=on-failure
Nice=5

[Install]
WantedBy=default.target
```

Then `systemctl --user enable --now magi-inference`. Open the listen port
in the firewall only for the networks of the callers.

## Use from Magi

Set `MAGI_PERSONA_MODELS` on the Magi server to a list of endpoints in
order of preference (see `deploy/render.py`, `PERSONA_LOCAL_URL`). Magi
tries the host one time and then uses the next endpoint.

## Tests

`go test ./...` uses the test binary as a fake engine and a fake sampler.
