# inference

The inference primitive runs a model engine on a machine that also does
other work, such as a desktop. It starts the engine when a request comes,
and stops it after an idle time. With `keep_warm`, it starts the engine
before the first request and keeps it loaded. When the machine is busy with other work,
it refuses new work, so that callers use a different provider.

The host is a plain process. It runs in the same way on a host and in a VM.
It does not need Kubernetes.

Contract: `contracts/inference.csil`, service `InferenceService`.

## API

| Request | Result |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions, forwarded to the engine |
| `GET /v1/models` | The model names of the host, with their descriptions ("Models") |
| `InferenceService/status` | State, reason, model, requests in flight |
| `InferenceService/decide` | Probabilities for typed questions about a state ("Decide") |
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

## Models

One engine can serve more than one model name: the base model, and the
base model with an adapter (llama-server per-request LoRA). Start the
engine with `--lora FILE --lora-init-without-apply`, and give each adapter
a name in `engine.models`:

```json
"engine": {
  "command": ["llama-server", "-m", "gemma-4-12b.gguf", "--lora", "notetaker-lora-v3.gguf",
    "--lora-init-without-apply", "..."],
  "port": 18091,
  "model": "gemma-4-12b",
  "description": "Gemma 4 12B. General chat, tools, and JSON schemas.",
  "models": {
    "gemma-4-12b-notetaker": {"lora": [{"id": 0, "scale": 1}],
      "description": "Tuned for the note taker (evals/notetaker, lora-v3). Other tasks: not measured."}
  }
}
```

A request for `gemma-4-12b-notetaker` goes to the engine as `gemma-4-12b`
with the `lora` field, and the answer names `gemma-4-12b-notetaker`. A
request for the base model has no adapter. A request with its own `lora`
field gets status 400: use a model name.

## Decide

`InferenceService/decide` answers typed questions about one state with
probabilities. The engine writes no text: the host reads the probability
of the first token of the answer for each label of the choices
(`evals/decide/README.md`).

| Type | Labels | Answer |
| --- | --- | --- |
| `noul` | `A` (yes), `B` (no) | `yes` or `no` |
| `choice` | `A` to `Z` (2 to 26 choices) | the choice |
| `score` | `0` to `max` (max at most 9) | the number, and `expected` (the mean) |

`temperature` is the calibration of a question for the model. Magi gives
it from the model catalog. The questions go one after the other, so that
the engine reuses the prompt of the state. For this, Gemma 4 needs
`--swa-full` (with `-c 12288 -np 4` on a 16 GB GPU). A model with
recurrent layers (Qwen 3.5) cannot reuse a part of a prompt; each question
reads the full state.

The host counts decide as its own work, as a chat request. Do not send
requests straight to the engine: the host then sees a busy GPU and stops
the engine.

## Busy rules

The host takes a sample every 5 seconds. The machine is busy when one of
these is true:

- A process with a listed name runs. The default list has compilers and
  linkers (`cc1`, `cc1plus`, `rustc`, `compile`, `ld`, `ld.lld`, `mold`,
  `javac`) and game processes (`gamescope`, `reaper`, `wineserver`).
- The CPU load is more than `cpu_load` (0.6). On Linux, this is the
  1-minute load average per CPU. On Windows, it is the CPU use (0 to 1)
  since the last sample.
- The GPU use is more than `gpu_util` percent (40) while the engine does
  not answer a request.
- The engine does not run, and the free GPU memory is less than `need_mib`
  (7000).

On Windows, the process names are the image names in lower case, without
`.exe`. The default list is `cl`, `link`, `rustc`, `compile`, `javac`,
`msbuild`, `vrserver`, and `vrcompositor` (SteamVR). Most games show as GPU
use.

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
  "keep_warm": true,
  "start_wait": "10s",
  "engine_log": "/path/to/engine.log",
  "rules": {"cpu_load": 0.6, "gpu_util": 40, "need_mib": 7000, "quiet_for": "60s",
            "processes": ["cc1plus", "rustc", "gamescope"]}
}
```

- `engine.command` must serve the OpenAI API and `GET /health` on
  `127.0.0.1:<port>`. The host starts it in its own process group and
  stops the group.
- `start_wait` is how long a request waits for a cold engine. With `0s`
  (the default), the host refuses at once with `warming`. Set it to a little
  more than the start time of the engine, also when the caller has another
  provider. A 9B model on one GPU starts in about 2 s, and a hosted
  provider can take 8 to 10 s to answer. Thus a short wait gives a faster
  reply than the fallback.
- `keep_warm`: the host starts the engine when the host starts, and each
  time the machine becomes quiet. It does not stop the engine for idle time
  (it ignores `idle_stop`). The busy rules still apply: when the machine is
  busy, the host stops the engine and gives the GPU back. Use this on the
  host of the model that Magi uses most, so that a reply does not wait for
  the model to load. If the engine fails, the host waits 1 minute before
  it starts the engine again.
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

## Connect out

A user machine connects only out. With `connect`, the host opens
connections to Magi and serves the same API on them (`lib/revconn`). Then
the machine needs no open port.

```json
"connect": {
  "url": "https://magi.example.com/inference/connect",
  "token_file": "/path/to/host-token",
  "idle": 2
}
```

- The token has the subject `inference:<name>` and the scope
  `inference:host`:
  `identity mint -subject inference:<name> -scopes inference:host -ttl 8760h -out <file>`.
  The host reads the file for each connection.
- The host renews the token before it expires (`primitives/identity`,
  "Machine tokens") and replaces the file. The account of the host must
  be able to write the directory of the file. `"no_renew": true` turns
  this off.
- The host keeps `idle` (2) idle connections. Each connection carries one
  request. Magi closes an idle connection after 4 minutes, and the host
  opens a new one. When Magi is not available, the host tries again after
  1 s, then up to each 30 s.
- With `connect` and no `listen`, the host does not listen.
- Magi still sends its `inference:use` token with each request, and the
  host checks it as before.
- In Magi, the endpoint of the host is `http://<name>.revconn/v1`
  (`PERSONA_LOCAL_URL` in `deploy/render.py`). When the host has no
  connection in the last minute, Magi uses the next endpoint at once.

## Windows

The host runs as a Windows service. Windows starts it when the machine
starts, also when no person logs in, and starts it again after a failure.

- Build: `GOOS=windows GOARCH=amd64 go build -o magi-inference.exe ./cmd/magi-inference`.
- The engine: a llama.cpp release for Windows with CUDA
  (`llama-<build>-bin-win-cuda-<version>-x64.zip` and the `cudart-*` zip in
  one folder).
- `nvidia-smi` comes with the NVIDIA driver.
- The host stops the engine with `taskkill /T /F`.
- `-log <file>` writes the log to a file. A service has no standard output.

Install, in PowerShell as Administrator:

```powershell
sc.exe create magi-inference binPath= "C:\magi\bin\magi-inference.exe -config C:\magi\config\config.json -log C:\magi\logs\host.log" start= auto obj= "NT SERVICE\magi-inference"
sc.exe failure magi-inference reset= 86400 actions= restart/5000/restart/30000/restart/60000
icacls C:\magi /grant "NT SERVICE\magi-inference:(OI)(CI)RX"
icacls C:\magi\logs /grant "NT SERVICE\magi-inference:(OI)(CI)M"
# The token: only the service and Administrators, and the service can replace it.
New-Item -ItemType Directory C:\magi\state
icacls C:\magi\state /inheritance:r /grant:r "NT SERVICE\magi-inference:(OI)(CI)M" "Administrators:(OI)(CI)F" "SYSTEM:(OI)(CI)F"
sc.exe start magi-inference
```

Put the token in `C:\magi\state\host-token` (`connect.token_file`).

`NT SERVICE\magi-inference` is a virtual account: it has no password and
only the rights of the folders above. `start= auto` starts the service
when Windows starts, before a person logs in. Until the network is up, the
host tries again to connect (see "Connect out"). With `delayed-auto`, the
service started 2 minutes and 17 seconds after Windows (measured).

## Use from Magi

Put each model name of the host in the model catalog
(`primitives/models`), with the endpoint `http://<name>.revconn/v1` for a
host that connects out, and `key_ref` `inference`. Magi selects the models
of each task from the catalog (`docs/models.md`).

Without a catalog default for a task, Magi uses `MAGI_PERSONA_MODELS` and
`MAGI_LIBRARIAN_MODELS`: lists of endpoints in order of preference (see
`deploy/render.py`, `PERSONA_LOCAL_URL`). Magi tries the host one time and
then uses the next endpoint.

## Tests

`go test ./...` uses the test binary as a fake engine and a fake sampler.
