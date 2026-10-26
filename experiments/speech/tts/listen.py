#!/usr/bin/env python3
"""Serve the local TTS listening set. This is not a production TTS API."""

import argparse
from functools import partial
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
from urllib.parse import unquote, urlsplit

ASSETS = Path(__file__).parent / "listen"


def catalog(root):
    runs = []
    for path in sorted(root.glob("*/manifest.json")):
        record = json.loads(path.read_text())
        # Package inventories and host details remain in the evidence manifest.
        runs.append({key: record.get(key) for key in (
            "engine", "status", "delivery", "prompts", "samples", "voices",
            "model_load_seconds", "peak_rss_kib", "threads", "scope",
        )} | {"id": path.parent.name})
    transcripts_path = root / "transcripts.json"
    transcripts = json.loads(transcripts_path.read_text()) if transcripts_path.is_file() else None
    return {"version": 1, "runs": runs, "transcripts": transcripts}


class Handler(BaseHTTPRequestHandler):
    def __init__(self, *args, results, **kwargs):
        self.results = results.resolve()
        super().__init__(*args, **kwargs)

    def do_GET(self):
        path = unquote(urlsplit(self.path).path)
        assets = {"/": ("index.html", "text/html; charset=utf-8"),
                  "/app.js": ("app.js", "text/javascript; charset=utf-8"),
                  "/style.css": ("style.css", "text/css; charset=utf-8")}
        if path == "/catalog.json":
            data = json.dumps(catalog(self.results), allow_nan=False).encode()
            kind = "application/json"
        elif path in assets:
            name, kind = assets[path]
            data = (ASSETS / name).read_bytes()
        elif path.startswith("/audio/"):
            parts = path.split("/")
            if len(parts) != 4 or any(p in ("", ".", "..") for p in parts[2:]):
                self.send_error(404)
                return
            target = (self.results / parts[2] / parts[3]).resolve()
            if (not target.is_relative_to(self.results) or target.suffix != ".wav"
                    or not target.is_file()):
                self.send_error(404)
                return
            data, kind = target.read_bytes(), "audio/wav"
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(data)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, default=ASSETS.parent.parent / "results" / "tts-2026-10-17")
    parser.add_argument("--port", type=int, default=8093)
    args = parser.parse_args()
    if not list(args.results.glob("*/manifest.json")):
        parser.error("No audition manifests found. Supply --results with a completed test directory.")
    try:
        server = ThreadingHTTPServer(("127.0.0.1", args.port), partial(Handler, results=args.results))
    except OSError as error:
        parser.exit(1, f"Cannot start the listening page: {error}. Select another --port.\n")
    print(f"TTS listening set: http://127.0.0.1:{server.server_port}/", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
