"""Fixed tool implementation. Runs only inside the admitted offline container."""
import json
import hashlib
import os
import selectors
import signal
import subprocess
import sys
import time

checked_digest = ""

def candidate_bytes(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        data = stream.read(16385)
    if len(data) > 16384:
        raise ValueError("file limit")
    return data

def main():
    raw = sys.stdin.buffer.read(20001)
    if len(raw) > 20000:
        raise ValueError("input limit")
    request = json.loads(raw)
    tool, args = request["tool"], request["arguments"]
    path = "/workspace/solution.py"
    if tool in ("read_file", "write_file") and args.get("path") != "solution.py":
        raise ValueError("path denied")
    if tool == "read_file":
        return candidate_bytes(path).decode("utf-8"), 0
    if tool == "write_file":
        data = args["text"].encode("utf-8")
        if not 0 < len(data) <= 16384:
            raise ValueError("file limit")
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        return "The candidate file was written.", 0
    if tool != "check" or args:
        raise ValueError("tool denied")
    # The test program arrives from the trusted executor, not a writable file.
    # Candidate Python is arbitrary code inside this container, not on the host.
    source = candidate_bytes(path)
    global checked_digest
    checked_digest = hashlib.sha256(source).hexdigest()
    # Check these exact bytes, even if a live human changes the file later.
    # Ordinary unit checks are not a defense against deliberate test tampering.
    test = "import types\nmodule=types.ModuleType('candidate')\nexec(compile(" + repr(source) + ", '/workspace/solution.py', 'exec'),module.__dict__)\n" + '''
cases=[(0,"0 ms"),(999,"999 ms"),(1000,"1.00 s"),(1250,"1.25 s"),(60000,"60.00 s")]
for value,expected in cases:
    actual=module.format_elapsed(value)
    if actual!=expected: raise AssertionError(f"format_elapsed({value}): expected {expected!r}, got {actual!r}")
for value in [-1, True, "10"]:
    try: module.format_elapsed(value)
    except (ValueError, TypeError): pass
    else: raise AssertionError("invalid input was accepted")
print("8 formatting checks passed")
'''
    child = subprocess.Popen([sys.executable, "-I", "-B", "-c", test], stdout=subprocess.PIPE,
                             stderr=subprocess.STDOUT, start_new_session=True, env={"LANG": "C.UTF-8"})
    captured = bytearray()
    selector = selectors.DefaultSelector()
    selector.register(child.stdout, selectors.EVENT_READ)
    deadline = time.monotonic() + 8
    try:
        while True:
            if time.monotonic() >= deadline:
                raise TimeoutError("check deadline")
            for key, _ in selector.select(.1):
                chunk = os.read(key.fd, 4096)
                if not chunk:
                    code = child.wait(timeout=1)
                    return captured.decode("utf-8", errors="replace"), code
                captured.extend(chunk)
                if len(captured) > 32768:
                    raise ValueError("check output limit")
    finally:
        selector.close()
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait()

try:
    text, code = main()
except Exception as error:
    text, code = "Tool failed: " + type(error).__name__, 1
sys.stdout.write(json.dumps({"outcome": "returned", "text": text, "exit_code": code,
                            "checked_digest": checked_digest}))
