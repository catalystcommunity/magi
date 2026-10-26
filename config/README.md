# Deployment configuration overlays

The repository contains only the public `catalystsquad.local` example.
Actual domains, addresses, registries, account settings, and profile locations
belong outside the checkout. The [example overlay](deployment.example.json)
defines the supported fields.

## Select an environment

An operator overlay is a JSON object that overrides fields in the example.
Set `kind` to `operator` and configure the target, endpoints, image repositories,
network ranges, storage class, gateway, and private paths. Unknown fields and
inconsistent cluster or registry settings are rejected.

For a new installation, copy the example to `~/.config/magi/deployment.json`
only if that file does not exist. Use directory mode `0700` and file mode
`0600`. Edit the copy, not the repository example.

The command wrapper selects that private file:

```sh
node deploy/run.mjs deploy/render.mjs 'REGISTRY/REPOSITORY@sha256:DIGEST'
```

This command renders resources. It does not apply them. Use a real immutable
image reference from the selected repository. Other operator commands use the
same wrapper. Live commands refuse the example configuration.

To select another overlay, set an absolute path:

```sh
export MAGI_DEPLOYMENT_CONFIG="/run/magi/deployment.json"
node deploy/run.mjs deploy/render.mjs 'REGISTRY/REPOSITORY@sha256:DIGEST'
```

A Reactorcide job can mount this file and its required profiles, then set the
same variable. No Reactorcide-specific source overlay is required. Mount only
the credentials needed by that job. Put paths for mounted profiles in the
overlay. Do not put secrets in the overlay itself.

Direct operator commands also accept `MAGI_DEPLOYMENT_CONFIG`. They do not
silently load a workstation environment. Unit tests and pure render functions
use the example by default. The core and release test commands explicitly
select the example, even if the caller selected an operator overlay.

## Private files

Default private locations are:

- `~/.config/magi/deployment.json`: the environment overlay.
- `~/.config/magi/release-policy.json`: the approver and release target.
- `~/.config/magi/profiles/qa/`: enrolled client and workflow profiles.
- `~/.config/magi/profiles/release/`: application keys, profiles, and local
  controller state.
- `~/.config/magi/profiles/speech/`: speech backend settings and client keys.
- `~/data/magi-evidence/deployments/default/`: image receipts and deployment
  records. The overlay can select an existing state directory.
- `~/data/magi-evidence/archives/`: private source and evidence archives.

`MAGI_OPERATOR_STATE_DIR` can override the record directory with an absolute
path. It does not change the release policy path. Local synthetic development
state can remain under ignored `.local/` directories.

Do not move an active database. Back up configuration and evidence on another
host. Preserve signed evidence bytes during migration.

## Release boundary

The [policy example](release-policy.example.json) uses synthetic identities.
It grants no authority. The configured policy must match the overlay's exact
domain, namespace, cluster, and image repository. The controller still permits
only the isolated canary, with no schema migration.

Approval, controller, and renewal manifests mount a separate environment
ConfigMap. Images contain code and the public example, not private settings.
Workstation paths and login hints are excluded from the mounted overlay.
Private runtime profiles still contain the approved `policy` object.

Before an image upgrade, supply both the environment mount and the private
runtime policy. Keep keys, approvals, and stored identities unchanged.
Configuration migration does not authorize a deployment.

## Local development and tests

The local Compose files require `MAGI_CORNDOGS_IMAGE`, an immutable image.
The wrapper sets it from the overlay:

```sh
node deploy/run.mjs scripts/dev-qa.mjs
node deploy/run.mjs scripts/dev-demo.mjs
```

Integration tests use the same image setting. Core and release unit tests do
not need a live environment. The example registry is a placeholder; it does
not provide downloadable images.

## Publication

Keep real files out of `config/`. Public JSON includes example overlays,
package metadata, model pins, compiler settings, and synthetic fixtures.
Ignore rules are not a complete secret scanner.

Run the [publication checks](../docs/publication.md), including a comparison
against the private overlay. Original deployment names and historical records
remain in the private archive. Public documents use example names.
