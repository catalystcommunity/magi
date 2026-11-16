# Runner trust tiers and publishing
Risk remediation 3.5 (requirement 9.5). A runner runs untrusted work (model
output, repository text) in a sandbox, and the same machine can hold Git
credentials. This page says what each runner kind protects, and which
runners may publish.
## Tiers
Lower number means stronger isolation. Magi cannot see a runner's sandbox
kind (the runner does not report it, and a report could not be trusted), so
the tier is a fact that the operator knows about the machine, and approval
for publishing (below) is the operator's statement that the tier is enough.
| Tier | Runner kind | What isolates the sandbox | Network and egress | Credentials on the runner host | Allowed actions |
|---|---|---|---|---|---|
| 1 | Linux task VM (Cloud Hypervisor, `sandbox.vm.kernel`) | Separate kernel, virtiofs share of the work directory only | Transparent route to the egress proxy only; no other route out | Runner token (`runner:<name>`, scope `magi:runner`); Git/`gh` credentials only if it publishes | prepare, work, check, merge/build, cleanup; publish only if approved |
| 2 | Linux bubblewrap with egress proxy | User, mount and network namespaces; `envd netns` gives a network namespace whose only route out is a unix socket to the proxy | Egress proxy only | Same as tier 1 | Same as tier 1 |
| 3 | macOS VM template (`sandbox.vm.template`, magi-vz) | Separate guest kernel | Transparent route to the egress proxy (Apple service hosts are blocked until allowed, see `docs/egress-proxy.md` section 6) | Same as tier 1 | Same as tier 1 |
| 4 | macOS Seatbelt (`sandbox-exec`) | Seatbelt profile: sees the work directory, read-only system directories, no home directory | **None.** Shares the host network. Magi refuses to start it unless `sandbox.allow_host_network` is set | Same, and the sandbox can reach anything the Mac can reach | prepare, work, check; publish **not** by default |
| 5 | Any runner with `sandbox.allow_host_network` | Whatever the kind gives | **None.** Direct network from the sandbox | Same | prepare, work, check; publish **not** by default |
What the runner host holds, for every tier: the runner token, the Git
credentials of the machine user (used by `publish`, never given to a
sandbox), and the `gh` login (used for pull requests). The sandbox never
sees these. The apprentice's model calls go through Magi, so the host holds
no provider key.
## What a runner may do
| Action | Step | Needs approval |
|---|---|---|
| Prepare, work, check, cleanup, merge in the sandbox | `prepare`, `work`, `check`, `merge`, `cleanup` | No |
| Push the branch, open the pull request, or fast-forward the upstream | `publish` | **Yes** |
| Read the pull request | `pr-check` | **Yes** |
| Merge the pull request | `pr-merge` | **Yes** |
| Upload a build of magi-server or runner packages that Magi releases and rolls out | artifact kinds `build`, `runner-package-build` (steps `merge` with `build: true`, and `release`) | **Yes** |
## Approval rule
Approval is a list in Magi's configuration, `PUBLISH_RUNNERS` in the values
file (`MAGI_PUBLISH_RUNNERS`, comma-separated runner names). A runner cannot
approve itself.
- **Default: empty. No runner publishes.** This fails closed.
- Magi picks a publisher only from approved runners. `MAGI_PUBLISHER` must
  also be approved, or starting a task fails with "publisher X is not
  approved to publish".
- Magi refuses a `Claim` of a `publish`, `pr-check` or `pr-merge` queue by
  an unapproved runner, refuses a `Complete` of those steps, and refuses an
  upload of a release build or a result that names one. Each refusal returns
  403 `runner X is not approved to publish` and logs
  `refused: runner is not approved to publish` with the runner and action.
  The `release` step also refuses the build of an unapproved runner.
- Wildcards are rejected at start-up.
- The runner's own `publish` and `pull_requests` settings in its
  configuration remain an extra limit: a runner publishes only when both
  Magi approves it and its own configuration says `publish: true`.
- **Minimum tier.** Tiers 1 to 3 may be approved. A tier 4 or tier 5 runner
  can publish only when the operator lists it on purpose, because Magi cannot
  tell its tier. Do not list a runner that has `allow_host_network` unless
  you accept that a prompt-injected sandbox could reach the network and the
  host's Git credentials path (credentials are not in the sandbox, but the
  sandbox has no egress limit).
- The `pr-merge` step runs on the working runner, so a runner that works on
  a pull-request task must be approved too. If it is not, the step waits
  unclaimed; merge the pull request on GitHub and choose `merged` in the
  `pr-wait` step.
Breaking change: before this rule, any connected runner with `publish: true`
could publish. Now set `PUBLISH_RUNNERS` for every runner that publishes,
and for every runner that builds a release (`build: true`).
