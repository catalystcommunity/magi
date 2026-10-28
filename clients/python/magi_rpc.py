"""HTTP transport for generated Magi Python clients.

A call is POST {base}/rpc/{Service}/{op} with a CBOR body. Errors come back
as a non-2xx status with a JSON body {"code": int, "message": str}.
"""
import json
import urllib.error
import urllib.request


class RpcError(Exception):
    def __init__(self, code: int, message: str):
        self.code = code
        self.message = message
        super().__init__(f"rpc {code}: {message}")


class HttpTransport:
    def __init__(self, base: str, token=None, timeout: float = 600.0):
        self.base = base.rstrip("/")
        self.token = token  # a str, or a callable that returns a str
        self.timeout = timeout

    def call(self, service: str, method: str, req: bytes) -> bytes:
        r = urllib.request.Request(
            f"{self.base}/rpc/{service}/{method}", data=req, method="POST",
            headers={"Content-Type": "application/cbor", "User-Agent": "magi-python/1"})
        tok = self.token() if callable(self.token) else self.token
        if tok:
            r.add_header("Authorization", "Bearer " + tok)
        try:
            with urllib.request.urlopen(r, timeout=self.timeout) as resp:
                return resp.read()
        except urllib.error.HTTPError as e:
            body = e.read()
            try:
                d = json.loads(body)
                raise RpcError(int(d.get("code", e.code)), str(d.get("message", "")))
            except (ValueError, AttributeError):
                raise RpcError(e.code, body.decode("utf-8", "replace").strip())
