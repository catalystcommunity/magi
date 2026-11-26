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
   and transcribed by the speech service. A client with a voice print of
   its person sends its check with the audio (`speaker`). For a turn that
   the client was not sure about, Magi checks the audio again with the
   voice profile of the person (`compose/voiceprint.go`,
   `docs/speech-interaction.md`, 3.2): the speech service gives the score
   of the audio, and of the audio with the other voices removed. With a
   score of 0.40 or more, Magi takes the turn and uses the audio with the
   better score. Else it keeps nothing of the turn (`ignored`). The
   message of a turn with a check has the annotation `speaker`.
   `get-voice-profile`, `put-voice-profile`, and `delete-voice-profile`
   read and change the profile of the caller in the speech service.
2. Magi posts the utterance to the stream of the person with the persona
   (default `persona:magi`), with the person's token. There are no chats:
   each person has one stream with each persona. Magi then starts a
   `persona-turn` run keyed by the utterance id.
3. A worker claims the step. It reads as the person through the persona
   (`person` and `persona` in each conversation call), so it sees only what
   this person told this persona, and what the audience of the persona
   shares. The model gets the newest 30 turns and "earlier, related"
   utterances: matches of the words of the new message, with the
   utterances that they link to and from. Each persona has the read
   tools (`docs/attention.md`, 4.1): the records of the person, the
   earlier conversation, the web, the tasks, and the facts of the
   installation. Each persona also has the tools of its work
   (`compose/personas.go`). The knowledge tools follow
   `docs/knowledge.md`: a correction is a `fix`, `update`, or `retract` of
   the record, not a second record.
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
  out as a bundle (`push-bundle`). Three failures stop the task. A failure
  with an environment error in the output (for example `Connection reset by
  peer`) goes to `blocked`, and does not count as a failure.
- `blocked` waits for `decide` from the run owner when the apprentice stopped
  without a result (a guard stop, a model endpoint that is down, or no money
  or quota left at the provider), or when the environment broke the check.
  `resume` starts another work round with the comment, `check` runs the
  checks on the work as it is, and `reject` ends the task. The work stays in
  the environment.
- `review` waits for `decide` from the run owner. A message is not a
  decision.
- `release` builds `magi-server` in the environment, pushes an image, and
  asks the release controller to roll it out. The controller rolls back a
  failed version. The step can resume after Magi itself restarts.

Environments never receive Git or registry credentials.

## Investigation workflow

An investigation is one question about a repository. An apprentice
answers it in an environment. The engineering persona starts it with the
tool `investigate` (`compose/investigation.go`).

```text
prepare -> investigate (apprentice, question mode) -> answer -> cleanup
   |                |                                  ^
   +---- fail ------+---- fail, blocked ---------------+
```

- `prepare` is the step of a code change: an environment with a clone of
  the repository. The run has the variable `mode` with the value
  `question`.
- `investigate` runs the apprentice in question mode
  (`primitives/apprentice/README.md`, "Question mode"). The apprentice
  reads the code and runs commands. Its summary is the answer, with the
  files and the lines that support it. The limits are 60 model calls and
  15 minutes.
- `answer` is a step of Magi. It keeps the answer as a page of the
  person. Then it starts a turn of the persona that asked (an event,
  `docs/decisions/0009-events-wake-personas.md`). The turn gets the
  answer and the link to the page, and it tells the person. When no turn
  can start, the person gets a notice with the link.
- `cleanup` removes the environment.

An investigation keeps no change. It has no check, no review, no merge,
no release, and no pull request. Its dispatch has no grants, thus its
environment gets no secret (no GitHub token). A failure of the
environment or of the apprentice goes to `answer`, which tells the person
that there is no answer. A person has at most three investigations at one
time. `list_tasks`, `task_status`, and `cancel_task` show and stop an
investigation.

On a runner, the step of the apprentice has the name `investigate`, not
`work`. A runner release from before this workflow does not claim it, so
it cannot make a change for a question. The run waits until the runner
has the new release.

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
- `sandbox.preflight` in the runner configuration is an optional shell
  command that runs in the sandbox before each work round, for example
  `curl -fsS -o /dev/null --max-time 20 https://proxy.golang.org/`. When it
  fails, the round does not start, and the task waits in step `blocked`
  with the output in `apprentice.error`.
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
  (outcome `proposed`). The person approved the change at the review
  step, so `pr-merge` merges the pull request at once, with no second
  question. `pr-merge` tries a merge commit first, then a rebase, then a
  squash, because a repository can permit only some of these. When that
  merge fails (a conflict, a review rule of the repository, no token), the
  task waits in `pr-wait`. Each 10 minutes
  `pr-check` reads the pull request on the same runner: merged goes to
  `rollout`, closed goes to `cleanup`, open waits again. The owner can
  decide `pr-wait` with `merge` (Magi tries again), `merged` (Magi checks
  it), or `closed`. A new push to the branch replaces it only when no one
  else pushed to it.
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
   installs PostgreSQL for the database tests of `tools/test-db.sh`, Cloud
   Hypervisor, and virtiofsd when the system has none, for task VMs.
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

### App build

A runner with the Android toolchains can build the Android app of a
merged change. Set `app` in the configuration of the runner:

```json
"app": {
  "sdk": "~/Android/Sdk",
  "gradle": "~/gradle-8.11.1",
  "java_home": "~/jdk17",
  "dep_cache": "~/.gradle/caches",
  "voice": "~/.cache/magi/android-voice",
  "keystore": "~/.android/debug.keystore"
}
```

- The merge step builds the app when the change touched `android/` or
  `clients/kotlin/`. The rollout step of Magi makes the build the app
  release. A phone with an older build then downloads it and asks its
  owner to install it (`docs/attention.md`, section 7).
- The build runs code of the change (Gradle scripts). It runs in a view
  of the host with no network and an empty home directory, as the checker
  does. The view has `sdk`, `gradle`, `java_home`, and the directory
  `modules-2` of `dep_cache`, all read-only.
- The build is offline. `dep_cache` is a Gradle cache directory that has
  the dependencies of the app. Build the app one time on the host to fill
  it (`tools/build-android.sh`).
- `voice` is the directory with the speech library and the voice models
  of the app. They are not in the repository. Run
  `tools/fetch-android-voice.sh` on the host one time, and again when
  `android/voice-pins.txt` changes. The view has the directory read-only.
  With no `voice`, or with old files, the build stops and the person gets
  the notice "App not built".
- The signing key is not in the view. The runner signs the package after
  the build. An update of an installed app needs the same key each time,
  so one runner builds the app. `keystore_pass_file` names a file with
  the password of the keystore. Without it, the password is that of an
  Android debug keystore. `key_alias` defaults to `androiddebugkey`.
- A change that touches the app on a runner with no `app` gets a note,
  and no app build.
- A build that fails does not stop the merge. The person gets the notice
  "App not built".
- `MAGI_TEST_APP_BUILD` (a JSON object as above) runs a real build in the
  tests of `magi/runner`.

### Checker

A runner can run a checker: a command of the runner host that checks a
change after its checks pass. The command is a coding agent of the person
who owns the runner, for example Codex, with the sign-in that this person
made on the host. The runner only starts the command. It does not read,
keep, or send the credentials of the command.

Set the command in the configuration of the runner:

```json
"checker": {
  "command": ["codex", "exec", "-m", "MODEL", "-c", "model_reasoning_effort=\"high\"", "-s", "workspace-write", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config",
              "--ignore-rules", "--color", "never", "--output-schema", "{schema}", "-o", "{out}", "-"],
  "timeout_minutes": 20,
  "writable": ["~/.codex"],
  "read_only": ["~/.cache/gomod"],
  "env": {"GOMODCACHE": "~/.cache/gomod", "GOPROXY": "off"},
  "env_files": {"AGENT_TOKEN": "~/.config/magi-runner/agent-token"}
}
```

- `--ignore-user-config` keeps the settings of the person out of the
  check, so name the model and the reasoning effort in the command.
- The command runs in a copy of the tree of the change, outside the
  sandbox of the task. `{dir}` is that directory, `{diff}` is the file with
  the diff, `{schema}` is the JSON Schema of the result, and `{out}` is the
  file where the command must write the result.
- The task and the instructions for the checker come on standard input
  (`runner/checker-prompt.md`).
- The result has a verdict (`approve` or `revise`) and findings. Each
  finding has a severity, a file, a line, the sentence of the task, the
  problem, and the evidence. The severity is `requirement` (a sentence of
  the task is not done for an ordinary input) or `edge` (an unusual input,
  a weak test, a setting of one host).
- The first `requirement` findings go back to the apprentice as the output
  of a failed check, one time. This does not count as a failed check.
- After that round, the findings are a hint in the review request. The
  `edge` findings are always only a hint: on fixed changes that a person
  approved (2026-11-22), a coding agent as checker named 4 to 6 findings
  for each change, and 0 or 1 of them had the severity `requirement`. A
  failure of the checker is also a hint. The checker does not stop a task.

The checker runs code of the change on the runner host. On Linux, the
runner starts the command in a view of the host (bubblewrap):

- The view has the system programs (`/usr`), the files of `/etc` for names
  and certificates, and the directory of the check.
- The home directory of the user is empty in the view. The command does
  not see keys, repositories, or other sign-ins.
- `writable` are the paths that the command can change: the directory
  where the coding agent keeps its sign-in. `read_only` are more paths
  that it can read: toolchains and a module cache, so that it can run the
  tests of the change.
- The command gets `HOME`, `PATH`, `USER`, `LANG`, `TERM`, and the
  variables of `env`. It does not get the variables of the runner.
- `env_files` gives a variable the text of a file of the host, for a
  coding agent that signs in with a token in a variable. Keep the file
  readable only by the user of the runner. The runner reads it when the
  checker starts. The value is not in the configuration, not in the
  argument list, and not in a report.
- The view keeps the network: the coding agent calls its service. So use a
  coding agent that has a sandbox with no network for the commands that
  it runs.
- A Linux host with no bubblewrap does not run the checker. macOS has no
  view yet: there the command runs with the small environment only.

A coding agent with no option for a result file prints the result. The
runner reads the printed JSON when the file `{out}` is not there. The
result can be the object of the schema, or an object that has it in the
field `structured_output`. `{schema_json}` is the text of the schema. This
example gives Claude Code a token from a file, and only tools that read:

```json
"checker": {
  "command": ["claude", "-p", "--model", "MODEL", "--effort", "high", "--tools", "Read,Grep,Glob", "--add-dir", "{dir}/..",
              "--output-format", "json", "--json-schema", "{schema_json}"],
  "read_only": ["~/.local/bin/claude"],
  "env": {"PATH": "~/.local/bin:/usr/bin:/bin"},
  "env_files": {"CLAUDE_CODE_OAUTH_TOKEN": "~/.config/magi-runner/claude-token"}
}
```

- `--add-dir` lets the agent read the diff, which is next to the tree.
- This checker reads the change and does not run it. It gives its token
  to each command that it starts, and the view has the network, so do not
  give it a tool that runs commands.

An agent that gets its token in a variable gives the token to each command
that it starts. Give such an agent no tool that runs commands, or a
sandbox with no network for them.

This example gives Claude Code a shell in its own sandbox, so that it can
run small tests of the change:

```json
"checker": {
  "command": ["claude", "-p", "--model", "MODEL", "--effort", "high", "--tools", "Read,Grep,Glob,Bash,Write,Edit",
              "--allowedTools", "Read,Grep,Glob,Write,Edit,Bash", "--add-dir", "{dir}/..",
              "--settings", "SETTINGS",
              "--output-format", "json", "--json-schema", "{schema_json}"],
  "timeout_minutes": 20,
  "read_only": ["~/.local/bin/claude", "~/.config/magi-runner/checker-settings.json", "~/.cache/gomod"],
  "env": {"PATH": "~/.local/bin:/usr/bin:/bin", "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": "1",
          "GOMODCACHE": "~/.cache/gomod", "GOPROXY": "off", "GOCACHE": "/tmp/gocache"},
  "env_files": {"CLAUDE_CODE_OAUTH_TOKEN": "~/.config/magi-runner/claude-token"}
}
```

`SETTINGS` is the full path of the settings file: the runner does not
change `~/` in the command. The settings file starts the sandbox of Claude
Code and refuses a command that cannot run in it:

```json
{"sandbox": {"enabled": true, "failIfUnavailable": true, "autoAllowBashIfSandboxed": true, "allowUnsandboxedCommands": false,
  "network": {"allowedDomains": [], "strictAllowlist": true},
  "filesystem": {"allowWrite": ["/tmp"]},
  "credentials": {"envVars": [{"name": "CLAUDE_CODE_OAUTH_TOKEN", "mode": "deny"}, {"name": "ANTHROPIC_API_KEY", "mode": "deny"}]}},
 "permissions": {"blockWritesOutsideWorkingDirectories": true}}
```

- The sandbox of Claude Code starts in the view of the runner. Its
  commands have no network and no token. They can write only in the
  directory of the check and in `/tmp`.
- The sandbox needs `bwrap` and `socat` on the host.
- On 10 fixed changes (2026-11-23), this checker approved the 3 changes
  that a person had approved and sent back the 7 changes with a defect.

The code of the change can read the paths of `writable`, which have the
sign-in of the coding agent. It has no network in the sandbox of the
coding agent, and the result of the checker has a fixed form.

### Planner

A runner can run a planner: a command of the runner host that reads the
repository before the first work round of a task. As the checker, it is a
coding agent of the person who owns the runner, and the runner only starts
it.

```json
"planner": {
  "command": ["claude", "-p", "--model", "MODEL", "--effort", "high", "--tools", "Read,Grep,Glob", "--output-format", "json"],
  "timeout_minutes": 10,
  "read_only": ["~/.local/bin/claude"],
  "env": {"PATH": "~/.local/bin:/usr/bin:/bin"},
  "env_files": {"CLAUDE_CODE_OAUTH_TOKEN": "~/.config/magi-runner/claude-token"}
}
```

- The command runs in a copy of the tree of the work directory, in the
  same view of the host as the checker. `{dir}` is that directory and
  `{out}` is a file where the command can write the text.
- The task and the instructions for the planner come on standard input
  (`runner/planner-prompt.md`).
- The result is text: acceptance checks with exact values, and a plan
  with the real names of the repository. The runner reads the file
  `{out}`, or the printed text, or the field `result` of a printed JSON
  object.
- The apprentice gets the text after the task, in each work round. The
  task decides when the two do not agree. The checker does not get it.
- The planner runs one time for a task. A planner that fails does not
  stop the task: the work starts without a plan.

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

## Dispatch and event turns

Work that continues after the turn or the request that started it (a
persona turn, a task, a to-do, a reminder) has a dispatch: the run
variable `dispatch` (`compose/dispatch.go`, `docs/decisions/0007-dispatch.md`).
It records the person for whom the work acts, the persona, the tools of
the persona, and the egress secret rules that applied at the start.

- Each step that a worker of Magi or a runner claims is checked. A run
  with no dispatch, with the dispatch of a different person, or with a
  grant that the egress rules do not give now is cancelled. The owner gets
  one notice.
- A turn has the tools of its dispatch that the persona still has. A task
  gets the secrets of its dispatch that the rules still give. A rule that
  was added later does not apply to work that started before.
- Work that a turn starts gets a dispatch from the dispatch of the turn.

A report of a task that is not `low` starts a turn of the persona that
started the task (`compose/events.go`, `docs/decisions/0009-events-wake-personas.md`).
Magi posts the event as a `system` utterance and starts a `persona-event`
run: a gate, then the turn. The event text has the id and the title of the
task, and not the text of the report: the persona reads the result with a
tool. The turn acts with the dispatch of the task. It cannot approve,
reject, or merge a task: only a message of the person can. It can send a
task back to the apprentice (`revise`, or `resume` for a blocked task).
Its reply goes to the devices of the person as a notice that is shown and
not said.

The gate is the loop limit:

- Chain: no more than `MAGI_EVENT_CHAIN` event turns (default 4) follow
  each other with no message of the person. The count includes the events
  of one task and the tasks that an event turn started. The next event
  starts no turn, and the person gets one notice.
- Rate: no more than `MAGI_EVENT_RATE` event turns (default 6) of one
  persona in one stream start in `MAGI_EVENT_WINDOW` (default `10m`).
  More events wait and start when the window has room. They do not wait
  for a person. The person gets one notice in a window.

`MAGI_EVENTS=off` starts no turn from an event.

The stall check (`compose/stall.go`, `docs/decisions/0012`) looks at each
task whose apprentice works, each `MAGI_STALL_EVERY` (default `1m`). It
cancels a task whose apprentice wrote no status line for
`MAGI_STALL_AFTER` (default `30m`), or whose last `MAGI_STALL_REPEAT`
status lines (default 6) have the same actions. The person gets the
reason as a notice, and the persona gets the event. Make the numbers
smaller where model capacity is small.

## Authorization

Operator calls (`put-runner-package`, `set-runner-release`,
`put-app-release`, `put-persona`) need the scope `magi:operate`, or a user subject in
`ADMINS` (`MAGI_ADMINS`). Any signed-in person can read the app release
and the list of runners. `get-access` tells a client if the caller is an
operator. Clients use it to show the admin views.

Calls for a person's own records use that person's token. Background work
uses the `service:magi` token with act-as scopes (see `deploy/README.md`).

Lexicon editors (`LEXICON_EDITORS`, `MAGI_LEXICON_EDITORS`; empty means the
admins) can change the household lexicon (`put-lexicon-word`,
`delete-lexicon-word`). Magi makes the change with its own token, which has
the scope `speech:lexicon`, and records the editor. Any signed-in person can
read the lexicon and hear text with it (`list-lexicon`, `speak`). The web
client shows the lexicon in the Words tab.

## Admin views

Operators see the Admin tab. The run and chat views (the ops in the table)
check the role of the caller for that view, and Magi logs each call with the
caller subject. The role of each view is fixed today; a later task reads the
roles from the database. The runner list (`list-runners`) needs no role:
any signed-in person can read it.

| Op | Returns | Role |
| --- | --- | --- |
| `admin-list-runs` | The runs of all people, newest first | `admin.tasks` |
| `admin-get-run` | One run: summary, run data, steps | `admin.tasks` |
| `admin-get-run-output` | One output of a run (check output, transcript) | `admin.tasks` |
| `admin-get-run-diff` | The patch and the file list of a run | `admin.tasks` |
| `admin-list-chats` | The chats of all people, newest first | `admin.chats` |
| `admin-read-chat` | One chat: summary and the newest messages | `admin.chats` |
| `admin-turn-log` | The turn log (the model calls) of a persona reply | `admin.chats` |

Magi reads the records with its service token (`workflow:act-as`). The runs
come newest first. The limit is 50 by default and 500 at most. There is no
filter on the owner: the workflow service does not offer one. Magi cuts each
value of the run data at 4096 bytes and adds a mark that says that it is cut.

The chat views read the conversation service with the service token of Magi
(`conversation:act-as`). `admin-list-chats` filters by `person` or `persona`.
`admin-read-chat` returns the newest messages of one chat. With `before_seq`,
it returns the messages before that seq (for scrolling back). The limit is 50
by default and 200 at most. `admin-turn-log` takes the id of the message of a
person. It returns the interpretation annotation of that message. The
annotation holds the log of the model calls of the persona reply. Admins read
private messages too; Magi logs each read.

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
    the policy of the proxy does not permit. With `auto` in the rules
    file, Magi permits some destinations with no person
    (`docs/egress-proxy.md`, section 4.2).
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
- When the runner restarts, it resumes the VM of each running task. The
  old virtiofsd of an environment can stay alive for a few seconds, and it
  holds the pid file next to its socket. Before a launch starts a new
  virtiofsd, it stops the processes that the pid files of the environment
  name, and it removes the old socket files. When two parts of the runner
  resume the same environment at the same time, one launch waits for the
  other. When virtiofsd still does not start, the launch runs once more
  after 2 seconds, and the error has the last lines of `envd.log`.

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
  "default": "model",
  "auto": {"sources": true, "hosts": ["*.mirror.example.org"], "model": true}
}
```

`auto` is optional. It lets Magi permit a destination that the policy of
the proxy does not list: the public software sources that Magi knows
(`sources`), the hosts of the installation (`hosts`), and a host that the
decision model knows (`model`; `limit` is the least probability, default
0.95).

The environment has only the placeholder (for example
`Authorization: Bearer magi-secret-gh`). The proxy puts the value in, only
for the hosts that its policy permits for that secret.

A secret rule can have:

- `personas`: only the tasks that one of these personas started get the
  secret (for example `["persona:engineering"]` for a GitHub token).
- `env`: a variable name (for example `GH_TOKEN`). The task environments
  and runner sandboxes that the rule applies to get this variable, with the
  placeholder as its value. Other tasks do not get the variable.

A review rule has the verdict `pass`, `review`, or `block`. Only `default`
can be `model`.

With `MAGI_MODEL_EGRESS=true`, the model clients of Magi (the persona,
the librarian, the review model, and `/runner/v1/chat/completions`) use
the egress proxy as the proxy user `svc_magi` and trust only the proxy CA.
The provider key can then be a proxy secret: `MAGI_MODEL_API_KEY`, and
`key` in `MAGI_PERSONA_MODELS`, hold a placeholder (`deploy/README.md`).

## Terminals

The owner of a task (or an admin, `MAGI_ADMINS`) can open a shell in the
sandbox of the task while the task runs or waits for review. `Task.terminal`
is true then. The clients:

- Web: the button "Open terminal" on the task, or the link
  `MAGI_PUBLIC_URL/#/terminal/RUN`. The link opens on any device and
  stays through the sign in. The page fits the terminal to the screen and
  has a row of keys that phone keyboards do not have (Esc, Tab, Ctrl, the
  arrows). The Android app opens this page in the browser.
- CLI: `magi terminal RUN`. The local terminal is in raw mode; Ctrl-C goes
  to the shell. Exit the shell to end the session. The command exits with
  the exit status of the shell.
- Personas: the engineering persona has the tool `terminal_link`; it gives
  the link.

The path:

1. The client opens the WebSocket `/api/terminal?task=RUN` on the public
   URL. The first message is the JSON hello `{"token", "cols", "rows"}`
   (the CLI sends its token in the `Authorization` header). Magi checks
   the token, the owner, and the status of the task. Then each WebSocket
   message is one terminal frame: the type byte and the payload, with no
   length (`primitives/execution/README.md`, Terminal).
2. A task on a runner: Magi cannot connect to the runner. Magi gives the
   session to the runner in the long poll `RunnerService/terminals`. The
   runner opens `GET /runner/terminal?session=ID` on Magi (an HTTP upgrade
   with the runner token), and copies that connection to `POST /v1/pty` of
   envd in the sandbox. Magi accepts the session only from the runner of
   the task.
3. A task in the cluster (no runner): Magi opens `GET /v1/pty` on the
   execution service with its service token.

Magi meets the two connections in the memory of the process. This needs
one Magi replica: with more replicas, a runner can reach another replica,
and the client gets an error after 20 s. When the runner does not connect
in 20 s (it is offline, or an older release), the client gets an error
frame; it does not wait longer.

The shell runs in the sandbox only, with the network of the sandbox: its
traffic goes through the egress proxy as the traffic of the task. Magi
logs `terminal opened` and `terminal closed` with the run, the owner, the
person, the runner, the reason, and the exit status. Magi closes a session
after 30 minutes with no input and after 8 hours in all. It sends a `k`
frame to the two sides each 30 s, so gateways do not close a quiet
connection. A reconnect starts a new shell; the earlier shell does not
continue.

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
magi terminal RUN              # a shell in the sandbox of a running task
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
(modality `text`), and the note taker records its facts as of that time:
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
time; default 4), `MAGI_EVENTS`, `MAGI_EVENT_CHAIN`, `MAGI_EVENT_RATE`, and
`MAGI_EVENT_WINDOW` (see Dispatch and event turns).

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
- Terminal sessions meet in the memory of one Magi process (see
  Terminals). A shell does not continue after a reconnect.
