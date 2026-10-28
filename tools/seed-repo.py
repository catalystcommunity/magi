#!/usr/bin/env python3
"""Create or update a source repository from the current working tree.

    tools/seed-repo.py --source http://127.0.0.1:58081 --token-file TOKEN [--name magi] [--update]

It copies the files that Git does not ignore into a temporary repository and
commits them. Without --update it imports that bundle with create-repo. With
--update it starts from the repository's main branch, commits the working
tree on top as "Sync from operator checkout", and imports it with push-bundle.
Fetch the repository's own commits first, or --update replaces them.
It does not change this checkout's Git state.
"""
import argparse, os, shutil, subprocess, sys, tempfile

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(root, "clients", "python"))
from magi_rpc import HttpTransport  # noqa: E402
from magi_source.client import SourceClient  # noqa: E402
from magi_source.types import BundleRequest, CreateRepoRequest, PushBundleRequest  # noqa: E402


def git(*args, cwd):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--source", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--name", default="magi")
    ap.add_argument("--update", action="store_true")
    a = ap.parse_args()
    client = SourceClient(HttpTransport(a.source, open(a.token_file).read().strip()))
    files = git("ls-files", "-z", "--cached", "--others", "--exclude-standard", cwd=root).split("\0")
    with tempfile.TemporaryDirectory() as tmp:
        work = os.path.join(tmp, "repo")
        if a.update:
            current = os.path.join(tmp, "current.bundle")
            open(current, "wb").write(client.bundle(BundleRequest(repo=a.name, refs=["main"])).bundle)
            git("clone", "-q", "-b", "main", current, work, cwd=tmp)
            for entry in os.listdir(work):
                if entry != ".git":
                    path = os.path.join(work, entry)
                    shutil.rmtree(path) if os.path.isdir(path) else os.remove(path)
        else:
            os.mkdir(work)
            git("init", "-q", "-b", "main", cwd=work)
        n = 0
        for f in files:
            src = os.path.join(root, f)
            if not f or not os.path.isfile(src) or os.path.islink(src):
                continue
            dst = os.path.join(work, f)
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            shutil.copy2(src, dst)
            n += 1
        git("add", "-A", cwd=work)
        if a.update and not git("status", "--porcelain", cwd=work).strip():
            print("no changes")
            return
        msg = "Sync from operator checkout" if a.update else "Magi source snapshot"
        git("-c", "user.name=Magi operator", "-c", "user.email=magi@example.local", "commit", "-q", "-m", msg, cwd=work)
        bundle = os.path.join(tmp, "repo.bundle")
        git("bundle", "create", "-q", bundle, "main", cwd=work)
        if a.update:
            res = client.push_bundle(PushBundleRequest(repo=a.name, bundle=open(bundle, "rb").read(), force=True))
            print("updated", [(r.name, r.commit[:12]) for r in res.updated])
            return
        repo = client.create_repo(CreateRepoRequest(name=a.name, default_branch="main", owner="service:magi", seed_bundle=open(bundle, "rb").read()))
        print(f"created {repo.name} from {n} files; clone path {repo.clone_path}")


if __name__ == "__main__":
    main()
