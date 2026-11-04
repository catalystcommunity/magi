# magi (composition layer)

This module connects the primitives into one system for people. It is the
only code that uses more than one primitive. It provides the human-facing
API in `contracts/magi.csil`, two clients, and the background workers.

## Parts

| Path | Purpose |
| --- | --- |
| `compose/conversation.go` | `MagiService`: say, read, search, trace, links, visibility, reviews, personas |
| `compose/service.go` | `MagiService`: tasks, decisions, knowledge, voice |
| `compose/context.go` | The context of a persona turn: related earlier utterances |
| `compose/librarian.go` | The librarian: links and visibility of each utterance |
| `compose/persona.go` | Workflow `persona-turn`: interprets one utterance with the model and replies |
| `compose/codechange.go` | Workflow `code-change` and its step workers |
| `compose/release.go` | Release step: build, push, and release through the release controller |
| `compose/worker.go` | Claim loop with heartbeats and progress notes |
| `model/` | OpenAI-compatible Chat Completions client |
| `cmd/magi-server` | The service process (API, web client, workers) |
| `cmd/magi` | Command-line client |
| `web/` | Browser client (SolidJS without Node; `tools/build-web.sh`, see `web/README.md`) |

## How a message flows

1. A client calls `say` with text or audio. Audio is stored as an artifact
   and transcribed by the speech service.
2. Magi posts the utterance to the stream of the person with the persona
   (default `persona:magi`), with the person's token. There are no chats:
   each person has one stream with each persona. Magi then starts a
   `persona-turn` run keyed by the utterance id.
3. A worker claims the step. It reads as the person through the persona
   (`person` and `persona` in each conversation call), so it sees only what
   this person told this persona, and what the audience of the persona
   shares. The model gets the newest 30 turns and "earlier, related"
   utterances: matches of the words of the new message, with the
   utterances that they link to and from. It can call `recall`,
   `remember`, `search_conversation`, `start_code_change`, `list_tasks`,
   and `cancel_task`.
4. The reply goes to the stream as a persona utterance, with a `reply-to`
   link, and a `started` link to each task that it started. The tool calls
   are kept as an `interpretation` annotation on the person's utterance.
5. The librarian classifies both utterances (see below).
   With `speak_reply`, the reply is also synthesized and stored as audio.

## Code-change workflow

```text
prepare -> work (apprentice) -> check --pass--> review (human) --approve--> merge -> release -> cleanup
              ^              |                   | revise                  |
              +-- fail-checks+                   +--> work                 +-- conflict -> cleanup
```

- `prepare` creates an environment, moves the repository in as a Git bundle,
  and makes branch `task/<run>`.
- `work` is done by the apprentice (queue `apprentice`).
- `check` runs `./tools/check.sh`. On success it commits and moves the branch
  out as a bundle (`push-bundle`). Three failures stop the task.
- `review` waits for `decide` from the run owner. A message is not a
  decision.
- `release` builds `magi-server` in the environment, pushes an image, and
  asks the release controller to roll it out. The controller rolls back a
  failed version. The step can resume after Magi itself restarts.

Environments never receive Git or registry credentials.

## Runners

A runner is a program on another machine that does the steps of code
changes there. It connects to Magi; Magi never connects to it, and it does
not listen on the network. Set `RUNNER` (`MAGI_RUNNER`) to the default
runner. A task can name another runner (`start -runner NAME`, or the
person names the machine to the persona).

```text
prepare -> work -> check --> review --> merge (and build) -> release -> publish -> rollout -> cleanup
  runner   runner   runner    person       runner            Magi      publisher    Magi      runner
```

- The runner says hello to Magi once a minute with its release, platform,
  repositories, and the repositories that it can publish. `magi runners`
  lists the connected runners.
- The runner claims steps from its own queues (`runner.<name>.<step>`)
  through `RunnerService` with a runner token (subject `runner:<name>`,
  scope `magi:runner`). It can act only on a step that it holds a claim for.
  The queues come from run variables (`runner.${runner}.prepare`), so one
  workflow definition serves all runners.
- Each task gets a sandbox (`primitives/execution/sandbox`): a task VM
  (Cloud Hypervisor) on Linux when `sandbox.vm.kernel` is set, otherwise
  bubblewrap; Seatbelt (`sandbox-exec`) on macOS. The sandbox sees its work
  directory and read-only system and tool directories, not the home
  directory of the runner user. The runner reads and writes sandbox files
  only through envd, inside the sandbox.
- The apprentice runs from the runner release (not from a checkout that a
  task can change). Its tools use the sandbox through an execution API on
  loopback. Its model calls go to `/runner/v1/chat/completions` on Magi,
  which adds Magi's provider key. The runner machine holds no provider key.
- The runner keeps a mirror of each repository with an upstream. It syncs
  before prepare and before merge. `merge` merges the reviewed branch onto
  the newest upstream commit in the sandbox, builds `magi-server` for the
  cluster (linux/amd64) and the runner packages, and uploads the merge as a
  Git bundle. Magi releases the build.
- `publish` runs on a runner that publishes the repository (`"publish":
  true`, or `PUBLISHER`). It pushes the merge bundle with that machine's Git
  credentials, only after the release stayed healthy, and only as a
  fast-forward. If the release rolls back, nothing is pushed. So a runner
  without Git credentials (a Raspberry Pi, a laptop) can do the work.
- `rollout` makes the new runner packages the runner release, when they
  differ from the current release. See "Runner updates".
- The review diff is the patch that the runner uploads.
- A runner removes the environments of runs that ended, also after a cancel.
- The commits of a task have the author in `author` (`name`, `email`) of
  the runner configuration. `tools/install-runner.sh` sets
  `Magi <magi@MAGI_HOST>`.

### Install a runner

Install one runner for each Magi installation. Names come from the Magi
host, so dev and production runners on one machine do not share files.

1. Install the sandbox toolchains (Go, Rust, and Python 3.11 or later when
   the system Python is older) on the runner machine, without root:
   `ssh USER@HOST 'bash -s' < tools/install-runner-tools.sh`. On Linux,
   bubblewrap must be installed (`apt install bubblewrap`). The script also
   installs Cloud Hypervisor, and virtiofsd when the system has none, for
   task VMs.
2. Make a runner token: `identity mint --subject runner:NAME --scopes
   magi:runner --ttl 2160h --out runner-NAME.token`.
3. Install the runner:

   ```sh
   tools/install-runner.sh magi.example.com --ssh USER@HOST --token runner-NAME.token \
     [--upstream URL] [--publish] [--service]
   ```

   Without `--ssh` it installs on this machine. `--publish` is only for a
   machine whose Git credentials can push to the upstream. A machine
   without them can fetch a public upstream over HTTPS.
4. Start the service. `--service` does it; or by hand:
   - Linux: copy `deploy/runner/magi-runner@.service` to
     `~/.config/systemd/user/`, then `systemctl --user enable --now
     magi-runner@magi.example.com`. For a machine without a login session,
     also `loginctl enable-linger USER` as root.
   - macOS: `--service` over SSH as the user, with root SSH to the same
     machine, installs `deploy/runner/magi-runner.plist` as a launchd
     daemon that runs as the user at boot.

To see the log of a runner, run `journalctl --user -u magi-runner@<magi
host>` (Linux) or read `~/Library/Logs/magi-runner-<magi host>.log`
(macOS).

### Runner updates

Magi tells runners which release to run. The service runs the launcher
(`~/.local/bin/magi-runner supervise`), and the launcher runs
`~/.local/share/magi-runner/<host>/current/magi-runner`, where `current`
links to one release in `releases/`.

1. A published change that built new runner packages becomes the runner
   release (step `rollout`). An operator can also set one: `go run
   ./cmd/runner-package -out DIR`, then `magi runner-release VERSION DIR`.
2. The runner stops claiming, waits until no step runs, downloads the
   package for its platform, checks the SHA-256 digest, unpacks it, and runs
   its self-test (a sandbox command, the apprentice import, and the token).
3. It points `current` at the new release and restarts into it.
4. The new release must say hello to Magi in 3 minutes, and must not exit
   twice before that. Otherwise the launcher points `current` back at the
   old release. The runner reports the error in its hello, and Magi does
   not offer that release to it again. `magi runners` shows the error.

The launcher itself changes only with `tools/install-runner.sh`.

## Authorization

Operator calls (`put-runner-package`, `set-runner-release`,
`put-app-release`, `put-persona`) need the scope `magi:operate`, or a user subject in
`ADMINS` (`MAGI_ADMINS`). Any signed-in person can read the app release
and the list of runners.

Calls for a person's own records use that person's token. Background work
uses the `service:magi` token with act-as scopes (see `deploy/README.md`).

Lexicon editors (`LEXICON_EDITORS`, `MAGI_LEXICON_EDITORS`; empty means the
admins) can change the household lexicon (`put-lexicon-word`,
`delete-lexicon-word`). Magi makes the change with its own token, which has
the scope `speech:lexicon`, and records the editor. Any signed-in person can
read the lexicon and hear text with it (`list-lexicon`, `speak`). The web
client shows the lexicon in the Words tab.

## Egress

The egress proxy (`primitives/egress`) is the only route out for task
environments with network `proxy` and for runner sandboxes. Magi has
three parts in it:

- **`EgressAskService`** is for the proxy and its plugins. It needs a
  service token with the scope `magi:egress` (`service:egress`).
  - `environment`: the owner and the task run of an environment (from the
    execution service). For a runner sandbox, the environment is the run,
    and the owner is the owner of the run. For `svc_<name>`, the owner is
    `service:<name>`.
  - `secret-rules`: the placeholder rules for the owner of a flow.
  - `evaluate`: a verdict for a flow (`pass`, `review`, or `block`).
    A flow of a Magi service (`svc_<name>`) passes: these are the model
    calls of Magi (`MAGI_MODEL_EGRESS=true`), and a review would call the
    model again. Then decided reviews come. Then the first rule that matches decides.
    When no rule matches, `default` decides. With `default: "model"`, the
    review model decides (the librarian chain: the local model first).
    The model must answer with a verdict tool call; another answer or an
    error is `review`.
  - `report`: the proxy blocked a flow for review (kind `request` or
    `destination`).
  - `destination`: whether a decided review permits a destination that
    the policy of the proxy does not permit.
- **Reviews** for admins: `list-egress-reviews` and `decide-egress-review`
  (`magi egress-reviews`, `magi egress-review`, the web tab Network, and
  the Android screen Network). The proxy does not wait for a person: it
  blocks at once, and the decision is a rule for the next such requests
  (`docs/egress-proxy.md`, section 4.1). Magi keeps reports and decisions
  in the artifacts service.
- **The runner route.** `RunnerService/egress-access` gives the runner
  that holds a step of a run the proxy user of that run (the run id, its
  credential, and the proxy CA). Without `MAGI_EGRESS_PROXY` it gives
  nothing, and sandboxes use the network of the runner machine.
  `GET /runner/egress` (runner token) is an HTTP upgrade that becomes one
  TCP connection to the proxy port. The runner machine thus connects only
  to Magi.

A runner sandbox (bubblewrap) with the proxy has its own network
namespace with no real interface. Its proxy variables point to
`127.0.0.1:3128` in the sandbox, and tools with no proxy setting reach the
proxy too (the transparent route, as in a task VM;
`primitives/execution/README.md`). envd copies each connection to a unix
socket, and the runner sends it through `/runner/egress`. envd
listens on a unix socket in the state directory of the sandbox, not on a
TCP port. Seatbelt (macOS) cannot isolate the network: a macOS runner
does not use the proxy.

### Task VMs (Linux)

A Linux runner with `/dev/kvm` can run each environment in a VM. Set
`sandbox.vm` in the runner configuration:

```json
"vm": {"kernel": "~/.local/share/magi-runner-tools/vm/vmlinux", "cpus": 4, "memory_mb": 8192}
```

- The kernel comes from `tools/vm/build-kernel.sh` (`vmlinux-x86_64` or
  `Image-arm64`). `hypervisor` and `virtiofsd` are optional; the defaults
  are the files that `tools/install-runner-tools.sh` installs.
- The runner user must be in the group `kvm`.
- On arm64, Cloud Hypervisor needs a GICv3 interrupt controller. A
  Raspberry Pi 5 has a GICv2, so it uses bubblewrap.
- The guest root is the bubblewrap view of the environment. virtiofsd
  shares it from inside its own bubblewrap sandbox. Thus the guest sees the
  same files as a bubblewrap sandbox, and nothing else of the host.
- envd is the init of the guest. The runner reaches envd over vsock.
- The guest has no network card. The guest DNS gives each name an address
  in `198.18.0.0/15` and keeps the name. The guest firewall sends each TCP
  connection to envd, which opens a `CONNECT name:port` tunnel through the
  proxy. Thus tools with no proxy setting also go through the proxy, and the
  proxy gets the name of each destination. The guest trusts the proxy CA in
  its system trust store. With no proxy, the guest has no network.
- Other DNS record types (for example TXT) go to the proxy, which answers
  only for permitted names. UDP does not leave the guest.
- SSH: the guest trusts the SSH host key of the proxy
  (`/etc/ssh/ssh_known_hosts`). The proxy ends SSH and runs each command on
  the host with its own key (`primitives/egress/README.md`, "SSH").

### Task VMs (macOS)

A Mac runner (Apple silicon) can run each environment in a macOS VM, for
example for iOS builds and the iOS Simulator. Linux tasks do not run on
macOS.

1. Build `magi-vz` on the Mac: `tools/vm/build-macos-vz.sh USER@HOST`. It
   writes `~/.local/share/magi-runner-tools/vm/magi-vz`, signed with the
   virtualization entitlement.
2. Get a restore image (IPSW) from Apple's catalog
   (`https://mesu.apple.com/assets/macos/com_apple_macOSIPSW/com_apple_macOSIPSW.xml`;
   check its `FirmwareSHA1`), then install the template:
   `magi-vz install --ipsw FILE --dir ~/.local/share/magi-runner-tools/vm/macos/template`.
3. As root on the Mac, with the VM stopped:
   `tools/vm/macos/prepare-template.sh DIR`. It adds a launchd daemon that
   starts envd from the share `magi` at boot.
4. For Xcode and iOS: provision the template with the sandbox test
   `TestMacProvision` (`MAGI_MAC_PROVISION=1`, `MAGI_TEST_MAC_TEMPLATE`,
   and `MAGI_MAC_RUNTIME_DMG`, a simulator runtime image of the host). In a
   clone it copies the host Xcode into the guest, selects it, accepts the
   licence, runs the Xcode first launch, adds the runtime, and boots a
   simulator. Then the clone becomes the template. Do it again after an
   Xcode update.
5. Set `sandbox.vm.template` in the runner configuration.

- Each environment is an APFS clone of the template, so it starts in about
  20 seconds and uses disk only for its changes.
- The guest sees the host only through shares at `/Volumes/My Shared
  Files`: `state`, `magi` (envd, the token, the proxy CA), and each
  `read_only` and `shared` directory by its base name. The work directory
  `/Users/Shared/work` is on the guest disk: files on a share have no
  owner, and Git refuses such repositories. The runner uses envd for it.
- The guest has no network card. With the egress proxy, tools use
  `127.0.0.1:3128` (the proxy variables), which envd copies over vsock to
  the runner. macOS has no transparent route.
- The macOS licence permits at most two macOS VMs at a time on a Mac. The
  runner refuses a third.
- Xcode is in the template, not in a share: macOS checks the signature of
  the whole Xcode bundle at the first start of a program in it, which takes
  about two minutes over a share. The check of the template stays valid in
  each clone.
- Host paths of `read_only` and `shared` directories in `sandbox.env` (for
  example in `PATH` or `GOMODCACHE`) become their guest paths.

The rules file (`MAGI_EGRESS_RULES`) has no secret values:

```json
{
  "secrets": [
    {"placeholder": "magi-secret-gh", "secret": "gh-token", "hosts": ["api.example.local"],
     "path": "^/repos/", "place": "header:authorization", "owners": ["user:a@example.local"]}
  ],
  "rules": [
    {"hosts": ["api.example.local"], "methods": ["GET"], "verdict": "pass", "reason": "reads"},
    {"hosts": ["*"], "methods": ["DELETE"], "verdict": "review"}
  ],
  "default": "model"
}
```

The environment has only the placeholder (for example
`Authorization: Bearer magi-secret-gh`). The proxy puts the value in, only
for the hosts that its policy permits for that secret.

With `MAGI_MODEL_EGRESS=true`, the model clients of Magi (the persona,
the librarian, the review model, and `/runner/v1/chat/completions`) use
the egress proxy as the proxy user `svc_magi` and trust only the proxy CA.
The provider key can then be a proxy secret: `MAGI_MODEL_API_KEY`, and
`key` in `MAGI_PERSONA_MODELS`, hold a placeholder (`deploy/README.md`).

## Librarian

The librarian runs in each Magi replica. It claims utterances that are not
classified (`claim-unclassified`, with a lease, so replicas do not do the
same one), and asks `MAGI_LIBRARIAN_MODELS` (usually only the local
inference host; default: the persona chain) for:

- links to earlier utterances of the stream and to older utterances with
  similar words, each with a kind, an optional part of the text, and a
  confidence;
- a visibility, when it differs from the default of the persona.

Only text of the person, and of the persona in the same stream, goes into
the prompt: shared messages of other members can carry instructions. A
JSON Schema limits the answer (link targets from the candidates only, the
known kinds, and the visibility values of the persona). The prompt includes
the person's recent answers and corrections. The conversation service
applies a link at or above the threshold of the person, and else makes a
review. It applies a more private visibility with no question. A message
becomes shared in a private persona only when the person answers. Answers and corrections move the
threshold (see `primitives/conversation/README.md`). When the model is
busy or fails, the claim ends after its lease and the utterance is tried
again. The `librarian` annotation on each utterance keeps the answer of the
model.

## Command-line client

```sh
go build -o magi ./cmd/magi
magi --server https://magi.example.local login you@example.local
magi chat                      # interactive; one stream, no chats
magi read -n 20; magi search readme; magi trace MESSAGE
magi links MESSAGE -all        # audit: links with their state and author
magi unlink LINK -m "not about the readme"; magi visibility MESSAGE private
magi reviews; magi answer REVIEW shared -m "a household fact"
magi personas; magi persona persona:butler Butler public user:a@x user:b@x   # operator
magi say -speak -audio question.wav
magi tasks; magi task RUN; magi diff RUN; magi decide RUN approve
magi cancel RUN -m "not needed"   # stop a task; asks first at a risky step
magi know calendar
magi usage -days 7             # your Magi replies by model
magi words; magi speak "Read the README, it takes 4-6 minutes" -o test.wav
magi word README -say readme; magi word Magi -heard Magei   # lexicon editor
magi unword README
magi egress-reviews            # admin: requests that the proxy blocked for review
magi egress-review egr_1f2e3d4c5b6a7980 allow-path -m "the package upload"
```

`login` prints a sign-in URL. Open it in a browser on any device. On a
desktop, the program also opens it. After the sign in, the browser shows a
one-time code. Paste it into the terminal, and the client gets its token.
The client does not listen on a local address, so the browser does not have
to be on the same machine.

## Configuration

`MAGI_LISTEN`, `MAGI_AUTH_KEYS`, `MAGI_SERVICE_TOKEN_FILE`, the `MAGI_*_URL`
endpoints of each primitive, `MAGI_MODEL_BASE_URL`, `MAGI_MODEL_NAME`,
`MAGI_MODEL_API_KEY_FILE`, `MAGI_ADMINS`, `MAGI_LEXICON_EDITORS`, `MAGI_REPO`, `MAGI_ENV_IMAGE`, `MAGI_CHECK_COMMAND`,
`MAGI_RELEASE_URL`, `MAGI_IMAGE_REGISTRY`, `MAGI_IMAGE_PULL_REGISTRY`,
`MAGI_IMAGE_CREDENTIALS_FILE`, `MAGI_IMAGE_BASE`, `MAGI_RELEASE_TARGET`,
`MAGI_EGRESS_URL` (the egress proxy API; set it when the installation has
the proxy), `MAGI_EGRESS_RULES` (the rules file; without it, `evaluate`
gives `review` for each flow), `MAGI_ENV_NETWORK` (the execution network of code-change
environments; the default is `proxy` when `MAGI_EGRESS_URL` is set, else
`egress`), `MAGI_EGRESS_PROXY` (host:port of the proxy port for runner
sandboxes), `MAGI_EGRESS_ENV_KEY_FILE` and `MAGI_EGRESS_CA` (the
environment key and the CA certificate of the proxy; with
`MAGI_EGRESS_PROXY`), `MAGI_MODEL_EGRESS` (`true`: the model calls go
through the proxy; see Egress), `MAGI_MODEL_API_KEY` (a key placeholder
for the proxy, in place of `MAGI_MODEL_API_KEY_FILE`).

## Model check

`TestLivePersonaTools` sends scripted messages to a model with the persona
prompt and tools. It checks the first tool call: `recall` before an answer
about the person's things, `remember` for new facts, `start_code_change`,
`list_tasks`, and no tool for small talk. Use it before you put a model in
`MAGI_PERSONA_MODELS`:

```sh
MAGI_TEST_PERSONA_URL=http://inference.example.local:8095/v1 \
MAGI_TEST_PERSONA_MODEL=qwen3.5-9b \
MAGI_TEST_PERSONA_KEY_FILE=/path/to/token \
go test -count=1 -v -run LivePersona ./compose/
```

On 2026-10-28, Qwen3.5 9B (Q4_K_M, RTX 4080) passed all 7 cases in 3 of 3
runs, with a mean of 0.6 s for each call.

`TestLiveLibrarian` checks a model as the librarian on 24 scripted
conversations with known answers: links (with parts), visibility for
private and public personas, and unclear cases where the right answer is a
question. It reports the share of correct decisions and the "confident
errors": wrong decisions with a confidence at or above the start threshold
(0.75), which nobody reviews. It uses `MAGI_TEST_LIBRARIAN_URL`,
`_MODEL`, `_KEY_FILE`, and `_RUNS` (default: the persona variables).

Results on 2026-10-31 (RTX 4080 16 GB, llama.cpp b12017, Q4_K_M, 5 runs):

| Model | Correct | Confident errors | Mean call |
| --- | --- | --- | --- |
| Qwen3.5 9B, current prompt | 87% | 13% | 1.1 s |
| Qwen3.5 9B, earlier prompt | 82% | 17% | 1.1 s |
| Qwen3.6 35B-A3B (32 expert layers on CPU) | 85% | 15% | 2.6 s |
| Gemma 4 12B, earlier prompt | 79% | 18% | 1.6 s |
| Qwen3.8 27B GSQ-RCO IQ3_XXS (10 GB) | 74% | 22% | 2.4 s |
| Qwen3.8 27B GSQ-RCO IQ3_S (2 runs, `-ngl 56`, some layers on CPU) | 81% | 18% | 6.3 s |

All models are too sure on unclear visibility (for example a member's
migraine that the household must know about): they give 0.8 to 1.0
instead of asking. The answers of the person raise the threshold for such
errors over time.

## Host setup

`magi-host-setup` (`cmd/magi-host-setup`) makes a Linux machine into a
model host. It runs deterministic checks, removes the engines and models
that do not fit, and then a local setup model (Qwen3.5 9B on the CPU) does
the setup. It writes the `engine` part of the inference host
configuration. Read `docs/host-setup.md`.

`TestLiveHostSetup` (`hostsetup/`) checks a setup model on 7 scripted
hosts. It uses `MAGI_TEST_SETUP_URL`, `_MODEL`, `_KEY_FILE`, and `_RUNS`
(default 2).

## Limits

- One replica. The persona and step workers are safe with more replicas,
  because claims are exclusive, but this is not tested.
- The release step builds and releases only `magi-server`.
- Task listings read the person's runs; there is no paging.
