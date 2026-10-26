"""One bounded model request. No tools, retries, credentials, or shared memory."""
import base64
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

MAX_RESPONSE = 65536


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def endpoint_ok(endpoint):
    u = urllib.parse.urlsplit(endpoint)
    return (len(endpoint) <= 2048 and u.hostname and not u.username and not u.password
            and not u.query and not u.fragment and not any(ord(c) <= 32 for c in endpoint)
            and (u.scheme == "https" or (u.scheme == "http" and u.hostname in ("127.0.0.1", "::1"))))


def execute(config):
    result = {"version": 1, "status_code": 0, "response": "", "error_code": "worker_input_invalid"}
    if set(config) != {"version", "endpoint", "request", "timeout_seconds"} or config["version"] != 1:
        return result
    if not endpoint_ok(config["endpoint"]) or not 0 < config["timeout_seconds"] <= 120:
        return result
    body = base64.b64decode(config["request"], validate=True)
    if not 0 < len(body) <= 16384:
        return result
    request = urllib.request.Request(config["endpoint"], data=body, headers={"Content-Type": "application/json"}, method="POST")
    # Do not inherit workstation proxies, redirect credentials, or retry an
    # uncertain call. Only the configured provider receives this request.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        try:
            response = opener.open(request, timeout=config["timeout_seconds"])
        except urllib.error.HTTPError as error:
            response = error
        with response:
            result["status_code"] = response.code
            raw = response.read(MAX_RESPONSE + 1)
            if len(raw) > MAX_RESPONSE:
                result["response"] = base64.b64encode(raw[:MAX_RESPONSE]).decode("ascii")
                result["error_code"] = "response_too_large"
                return result
            result["response"] = base64.b64encode(raw).decode("ascii")
            result["error_code"] = "" if response.code == 200 else "provider_http_" + str(response.code)
    except (OSError, ValueError, urllib.error.URLError):
        result["error_code"] = "provider_connection_failed"
    return result


def main():
    try:
        raw = sys.stdin.buffer.read(32769)
        if len(raw) > 32768:
            raise ValueError("input limit")
        result = execute(json.loads(raw))
    except (ValueError, TypeError, KeyError, AttributeError):
        result = {"version": 1, "status_code": 0, "response": "", "error_code": "worker_input_invalid"}
    sys.stdout.write(json.dumps(result, separators=(",", ":")))


if __name__ == "__main__":
    main()
