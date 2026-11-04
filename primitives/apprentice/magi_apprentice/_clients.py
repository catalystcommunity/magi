"""Import the generated Magi clients and build transports.

In the image the clients are on PYTHONPATH. In a checkout they live in
clients/python at the repository root; this module adds that path.
"""
from __future__ import annotations

import sys
from pathlib import Path
from typing import Callable

try:
    import magi_rpc  # noqa: F401
except ImportError:  # running from a checkout
    _repo_clients = Path(__file__).resolve().parents[3] / "clients" / "python"
    if _repo_clients.is_dir():
        sys.path.insert(0, str(_repo_clients))

import magi_artifacts as artifacts  # noqa: E402
import magi_execution as execution  # noqa: E402
import magi_workflow as workflow  # noqa: E402
from magi_rpc import HttpTransport, RpcError  # noqa: E402

__all__ = ["artifacts", "execution", "workflow", "HttpTransport", "RpcError",
           "token_source", "transport"]


def token_source(path: str | None) -> Callable[[], str | None]:
    """Return a callable that reads the bearer token file on each call, so a
    rotated token is used without a restart."""
    def read() -> str | None:
        if not path:
            return None
        try:
            return Path(path).read_text().strip() or None
        except OSError:
            return None
    return read


def transport(url: str, token_file: str | None, timeout: float = 600.0) -> HttpTransport:
    return HttpTransport(url, token=token_source(token_file), timeout=timeout)
