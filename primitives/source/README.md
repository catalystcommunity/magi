# source

The source primitive hosts Git repositories. It shows diffs, merges
branches, and moves code in and out as Git bundles. The same process serves
Git smart HTTP, so clients can clone and push.

## API

Contract: `contracts/source.csil`, service `SourceService`.

| Operation | Behavior |
| --- | --- |
| `create-repo` | Make an empty bare repository. The default branch is `main`. With `seed_bundle`, import the `refs/heads/` and `refs/tags/` refs of the bundle. |
| `get-repo`, `list-repos` | Return repository metadata. `list-repos` returns the repositories of the caller, of `owner` (act-as or admin), or all (admin without `owner`). |
| `list-refs` | Return all refs and their commits. |
| `diff` | Show changes with `git diff base...head` (three dots): the merge base of `base` and `head` is compared with `head`. `base_commit` is the commit of `base`. The patch is cut at `max_bytes` (default 1 MiB, maximum 16 MiB) and `truncated` is set. |
| `merge` | Merge `source` into the branch `target` without a work tree (`git merge-tree --write-tree`). A fast-forward moves the ref. A conflict returns `merged: false` and the conflicted paths in `message`. `fast_forward_only` refuses other merges. |
| `delete-branch` | Remove a branch. The default branch cannot be removed. |
| `bundle` | Return `git bundle create` of the branches in `refs`, or of all refs. |
| `push-bundle` | Import the `refs/heads/` refs of a bundle into an existing repository. An update to the default branch or a non-fast-forward update needs `force`. Creating the default branch in an empty repository does not need `force`. All updates apply in one transaction. |

## Git smart HTTP

- Clone path: `/git/<name>.git`. Only the smart protocol is served.
- Use Basic authentication. The user name can be any value. The password is
  a Magi bearer token.
- Clone and push need the same access as the RPC operations.

## Access

- A repository has an owner. By default, the owner is the caller.
- The owner, a caller with `source:act-as`, and a caller with `source:admin`
  can read and change a repository.
- `source:act-as` or `source:admin` is necessary to set `owner` to another
  subject.
- Other callers get "not found".

## Names

- Repository names must match `^[a-z0-9][a-z0-9._-]{0,63}$` and must not end
  with `.git`.
- Branch names are checked with `git check-ref-format`.
- User input does not go to a shell. Revisions are resolved with
  `git rev-parse --verify --end-of-options` before use.

## Storage

Each repository is a bare repository `<MAGI_SOURCE_DIR>/<name>.git`. Its file
`magi.json` holds the owner, the default branch, and the creation time. No
database is necessary.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | none (required). Comma-separated issuer key files. |
| `-source-dir` | `MAGI_SOURCE_DIR` | `/var/lib/magi/source` |

The process needs `git` 2.38 or later on `PATH`. RPC requests can be up to
256 MiB, for bundles.

## Run

```sh
MAGI_AUTH_KEYS=/etc/magi/issuer.json MAGI_SOURCE_DIR=/srv/source go run ./cmd/source
git clone http://x:$MAGI_TOKEN@source.example.local:8080/git/demo.git
```

Image (build from the repository root):

```sh
docker build -f primitives/source/Dockerfile -t magi-source .
```

## Test

```sh
go vet ./... && go test ./...
```

The tests use the real `git` binary, an `httptest` server, and the generated
client. They clone and push over smart HTTP, and they test diff, clean and
conflicting merges, bundles, and access denials. They skip when `git` is not
installed.

## Limits

- One process owns the directory. Locks that serialize ref changes are in
  memory. Do not run two replicas on the same directory.
- There is no branch protection except for the default branch rules of
  `push-bundle` and `delete-branch`. Smart HTTP push can change any branch.
- Repositories cannot be deleted or renamed through the API.
- Response bundles are held in memory.
