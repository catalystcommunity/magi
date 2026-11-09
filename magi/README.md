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
| `compose/personas.go` | The personas: `persona:magi` (general), `persona:assistant` (executive assistant), `persona:engineering` (engineering manager), with their prompts and tools |
| `compose/plans.go` | Workflow `todo`: to-dos, reminders, and plans of a person (`docs/plans.md`) |
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
   `remember`, `change_knowledge`, `search_conversation`,
   `start_code_change`, `list_tasks`, and `cancel_task`. The knowledge tools
   follow `docs/knowledge.md`: a correction is a `fix`, `update`, or
   `retract` of the record, not a second record.
4. The reply goes to the stream as a persona utterance, with a `reply-to`
   link, and a `started` link to each task that it started. The tool calls
   are kept as an `interpretation` annotation on the person's utterance.
5. The librarian classifies both utterances (see below).
   With `speak_reply`, the reply is spoken in parts (`compose/speech.go`):
   Magi posts the text at once, with modality `voice`. Then it synthesizes
   the first clause, then each sentence, and adds each part as a
   `speech-part` annotation (audio digest, offsets in the text, word
   times). `listen` gives the parts to a client while Magi makes them; the
   first part is ready about 350 ms after the text. When all parts are
   made, the message gets the whole audio (`set-audio`) for clients that do
   not listen. `stop-speech` stops the parts that are not made yet, and
   `report-delivery` records how much of the reply the client played
   (`delivery` annotation; see `docs/speech-interaction.md`, 2.3.1).

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
- With `"pull_requests": true` on the repository of the publishing runner,
  `publish` pushes the merge to the branch `magi/<task>` of the GitHub
  upstream and opens a pull request with the `gh` command of that machine
  (outcome `proposed`). The task then waits in `pr-wait`. Each 10 minutes
  `pr-check` reads the pull request on the same runner: merged goes to
  `rollout`, closed goes to `cleanup`, open waits again. The owner can
  decide `pr-wait` with `merged` (Magi checks it) or `closed`. A new push to
  the branch replaces it only when no one else pushed to it.
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
- **Inference hosts.** `GET /inference/connect` (subject
  `inference:<name>`, scope `inference:host`) is an HTTP upgrade. Magi
  keeps the idle connection, and a model endpoint
  `http://<name>.revconn/v1` sends each request on one of them
  (`lib/revconn`, `primitives/inference`, "Connect out"). These requests
  do not go through the egress proxy.

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
- The guest has no network card. With the egress proxy, tools can use
  `127.0.0.1:3128` (the proxy variables), which envd copies over vsock to
  the runner. Other TCP connections take the transparent route: envd
  (root, from launchd) adds routes 0/1 and 128/1 on `lo0`, a pf rule
  (anchor `com.apple/250.magi`) that sends them to its port 15001, and a
  resolver for all domains at its DNS (`scutil`, `SupplementalMatchDomains
  ""`; programs on macOS ask mDNSResponder, not `/etc/resolv.conf`). It
  finds the destination with `DIOCNATLOOK`. SSH to an `ssh_hosts` host of
  the proxy works: envd puts the proxy host key in
  `/etc/ssh/ssh_known_hosts`. macOS itself also connects out (Apple
  services); the proxy blocks these, and an admin can block the hosts once
  to stop the reviews.
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
magi know calendar; magi know -past dentist
magi remember me dentist "Dr. Rivera" -object-kind person
magi fix clm_x Phoenix -m "misheard"; magi update clm_y "Dr. Chen"; magi retract clm_z
magi merge ent_keep ent_dup -m "the same dentist"; magi history clm_x
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

## Note taker

In a persona turn that can record knowledge (`remember`), the note taker
reads the newest message, the two messages before it, and the matching
records. It asks `MAGI_LIBRARIAN_MODELS` for the knowledge operations of
the message, as JSON that agrees with a schema: `remember`, `update`,
`fix`, or `retract`, with the arguments of the tools. Magi applies them
with the tool functions, before the persona model writes the reply, and
tells the persona model what it did, with the line of each changed record.
Then Magi finds the matching records again, so that the persona model gets
the records after the changes. Thus a fact is recorded also when the
persona model does not call a tool.

An earlier answer of the persona can be wrong after a change of the
records. Each persona reply keeps the records that it got (the records of
the prompt and the `recall` results) as attachments of kind
`knowledge-record`: the record ID, and the line of the record as the title.
At most 60 are kept. They are not links. For each turn, Magi gets each
`fix`, `update`, `retract`, `merge`, and `unmerge` of the person's knowledge
since the oldest message of the history (`timeline`). A retract also covers
the records in its `after.records`.

- An earlier answer that used a record that changed after the answer: Magi
  does not give its text to the model. It gives a note in its place, for
  example: "[This answer was superseded by a later change of the records
  (the person said: "There is no chores calendar. Forget it.") retracts it:
  it was never true, or the person asked to forget it. Do not name or
  repeat what was forgotten. Do not repeat the earlier answer; use the
  current records.]" For a fix or an update, the note also gives the old
  record ("The record was: ..."). For a retraction it does not: the person
  wants it forgotten.
- An earlier answer with no `knowledge-record` attachments (a reply from
  before this change), before the newest change: it starts with "[Before a
  change of the records: this answer can be out of date.]".

The heading of the earlier, related messages tells that the records
changed. Magi removes the note and the mark from a reply that copies them.
The turn annotation has each call with
`"by": "note-taker"` and the model call in `notes`.
`MAGI_NOTE_TAKER=false` turns it off. See `docs/knowledge.md`.

## Web search

A persona can look up facts with two tools. It does not reach the web
itself; Magi does it, in these steps:

1. `web_search` (query, why). Magi gives the query, the reason, and the
   newest message of the person to `MAGI_LIBRARIAN_MODELS` (the judge, a
   schema answer). The judge refuses a private fact of the person ("where
   does my brother live": the records are the source), a query with
   private details that the search does not need (it writes the query
   again without them), and searches for serious harm. Magi also removes
   email addresses, phone numbers, and record IDs. When the judge does not
   answer, there is no search (fail closed).
2. Magi sends the query of the judge to SearXNG (`MAGI_SEARCH_URL`). SearXNG
   calls the engines through the egress proxy as `svc_searxng`.
3. Magi keeps at most 8 results: only `http` and `https` on ports 80 and
   443, public host names and addresses, no duplicates, short text. The
   judge model keeps the results that relate (at most 6) and drops text
   that gives orders to an AI. The persona gets the results as data, with
   the instruction to name the source.
4. `read_page` (url) reads only a page from the results of this turn or a
   page that the person sent. Magi refuses a host that resolves to an
   address that is not public, and a redirect to one. The page goes
   through the egress proxy as `svc_search` (the injection detector of the
   proxy scans it). Magi gives only the visible text (no scripts, styles,
   or hidden elements), at most 8,000 characters.

At most 6 searches and 4 pages in one turn.

The personas look things up and do not say that they cannot answer
(`lookupRules`). When a reply says "I don't know", "I can't look that up",
or "I don't have real-time access", and the turn made no search, Magi asks
the model one time to search and answer (`cantAnswerNote`). Magi does not
ask when a `recall` of the turn found nothing: that is a private fact that
the records do not have, and the reply stays "I don't know" with a
question to the person. For a question that no one can answer for certain, the persona
gives the main views and the reasoning.

Without `MAGI_SEARCH_URL`, the personas have no web tools.

## Working indicator

A client shows that Magi builds a reply: the newest message of the stream
is from a person, it is less than 3 minutes old, and no persona reply came
after it (a notice is not a reply). Every client reads the same stream, so
every device shows the same state. The web client turns the orb and shows
"<persona> is working on it"; the Android client shows a progress bar; the
command line shows a spinner on standard error (on a terminal only).

A persona turn notes each step in its workflow run ("thinking", "checking
the calendar", "reading the Longhouse tasks", "putting the answer
together"). `activity` gives the newest note of the caller's running turn.
The clients read it every 2.5 seconds while they wait and show it in place
of "working on it" ("Beatrice is checking the calendar"). A turn that still
notes keeps the indicator on after 3 minutes, so a long look up shows as
busy. `magi activity` prints it.

## History import

`import-history` takes the caller's earlier messages (at most 20 in one
call) with their times. Each message goes into the stream with its time
(modality `import`), and the note taker records its facts as of that time:
a new fact is valid from the time of the message, and a change happens at
that time. Magi makes no reply. The note taker always gets the date of the
message, also in live turns, so that "next Tuesday" has a date.

When Magi accepts a spoken message, or a typed message with spoken
replies, the device that sent it plays the acknowledgement tone: one short
rising tone (880 to 1320 Hz, 0.1 s). It is different from the notice tone
(two tones). A message that the wake word filter drops gets no tone.

## Health and release checks

- `GET /healthz`: the process is ready. The readiness probe uses it.
- `GET /healthz/deps`: one read operation of each service that Magi uses
  (workflow, conversation, knowledge, artifacts, execution, source, speech,
  release), with the clients of this build. 200, or 503 when one fails.
  The answer has only `name ok|fail time`; the log has the errors. The
  result is kept for 2 seconds. The release controller uses it during the
  stabilization window.
- `magi-server preflight`: the same calls, then exit code 0 or 1. It starts
  no workers. The release controller runs it before it changes the
  Deployment (`primitives/release`, `deploy/README.md`).

An answer that this build cannot decode fails the check. This finds a
client of a newer contract and a server of an older one.

## Configuration

`MAGI_LISTEN`, `MAGI_AUTH_KEYS`, `MAGI_SERVICE_TOKEN_FILE`, the `MAGI_*_URL`
endpoints of each primitive, `MAGI_MODEL_BASE_URL`, `MAGI_MODEL_NAME`,
`MAGI_MODEL_API_KEY_FILE`, `MAGI_NOTE_TAKER`, `MAGI_ADMINS`, `MAGI_LEXICON_EDITORS`, `MAGI_REPO`, `MAGI_ENV_IMAGE`, `MAGI_CHECK_COMMAND`,
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
for the proxy, in place of `MAGI_MODEL_API_KEY_FILE`), `MAGI_SEARCH_URL`
(SearXNG for web search; see Web search), `MAGI_TIME_ZONE` (the IANA time
zone of the household, for the time in the persona prompt and the times of
to-dos; default UTC), `MAGI_PERSONA_WORKERS` (persona turns at the same
time; default 4).

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
  because claims are exclusive, but this is not tested. A replica runs
  `MAGI_PERSONA_WORKERS` persona turns at the same time (default 4). The
  turns of one stream run in order in one replica only: with more
  replicas, two turns of one stream can run at the same time.
- The release step builds and releases only `magi-server`.
- Task listings read the person's runs; there is no paging.
