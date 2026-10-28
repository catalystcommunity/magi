"""OpenAI-compatible Chat Completions adapter with tool calling.

Works with llama.cpp llama-server (--jinja) and hosted providers that speak
the same API. Only configuration differs.
"""
from __future__ import annotations

import json
import random
import re
import socket
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass, field
from typing import Any, Callable, Protocol

from .config import ModelConfig

RETRY_STATUS = {408, 409, 425, 429, 500, 502, 503, 504}


class ModelError(Exception):
    pass


@dataclass
class ToolCall:
    id: str
    name: str
    arguments: str  # JSON text, as the model produced it


@dataclass
class Reply:
    content: str
    tool_calls: list[ToolCall]
    finish_reason: str = ""
    usage: dict = field(default_factory=dict)
    raw: dict = field(default_factory=dict)

    def message(self) -> dict:
        """The assistant message to append to the conversation."""
        m: dict[str, Any] = {"role": "assistant", "content": self.content or ""}
        if self.tool_calls:
            m["tool_calls"] = [
                {"id": c.id, "type": "function",
                 "function": {"name": c.name, "arguments": _history_args(c.arguments)}}
                for c in self.tool_calls]
        return m


def _history_args(arguments: str) -> str:
    """Providers refuse a history with tool arguments that are not JSON. The
    tool result still reports the original error to the model."""
    try:
        json.loads(arguments)
        return arguments
    except ValueError:
        return "{}"


class Model(Protocol):
    def complete(self, messages: list[dict], tools: list[dict]) -> Reply: ...


# Some servers leave tool calls in the text when their parser misses them.
_XML_CALL = re.compile(r"<tool_call>\s*<function=([\w.-]+)>(.*?)</function>\s*</tool_call>", re.S)
_XML_PARAM = re.compile(r"<parameter=([\w.-]+)>\n?(.*?)\n?</parameter>", re.S)
_JSON_CALL = re.compile(r"<tool_call>\s*(\{.*?\})\s*</tool_call>", re.S)


def parse_text_tool_calls(text: str) -> tuple[str, list[ToolCall]]:
    calls: list[ToolCall] = []
    for name, body in _XML_CALL.findall(text):
        args = {k: v for k, v in _XML_PARAM.findall(body)}
        calls.append(ToolCall(_new_id(), name, json.dumps(args)))
    if not calls:
        for blob in _JSON_CALL.findall(text):
            try:
                d = json.loads(blob)
                a = d.get("arguments", {})
                calls.append(ToolCall(_new_id(), str(d["name"]),
                                      a if isinstance(a, str) else json.dumps(a)))
            except (ValueError, KeyError, TypeError):
                continue
    if calls:
        text = _JSON_CALL.sub("", _XML_CALL.sub("", text)).strip()
    return text, calls


def _new_id() -> str:
    return "call_" + uuid.uuid4().hex[:16]


class ChatModel:
    """Chat Completions client using urllib only."""

    def __init__(self, cfg: ModelConfig, sleep: Callable[[float], None] = time.sleep,
                 opener: Callable[..., Any] = urllib.request.urlopen):
        self.cfg = cfg
        self._sleep = sleep
        self._open = opener

    def complete(self, messages: list[dict], tools: list[dict]) -> Reply:
        body: dict[str, Any] = {"model": self.cfg.name, "messages": messages,
                                "max_tokens": self.cfg.max_tokens}
        if tools:
            body["tools"] = tools
            body["tool_choice"] = "auto"
        if self.cfg.temperature is not None:
            body["temperature"] = self.cfg.temperature
        body.update(self.cfg.extra)
        data = self._post(json.dumps(body).encode())
        return self._parse(data)

    def _post(self, payload: bytes) -> dict:
        url = self.cfg.base_url.rstrip("/") + "/chat/completions"
        last = ""
        for attempt in range(self.cfg.max_retries + 1):
            req = urllib.request.Request(url, data=payload, method="POST",
                                         headers={"Content-Type": "application/json",
                                                  # Some providers block the default urllib agent.
                                                  "User-Agent": "magi-agent/1"})
            if self.cfg.api_key:
                req.add_header("Authorization", "Bearer " + self.cfg.api_key)
            delay = min(30.0, 1.5 * 2 ** attempt) * (0.5 + random.random() / 2)
            try:
                with self._open(req, timeout=self.cfg.timeout_s) as resp:
                    return json.loads(resp.read())
            except urllib.error.HTTPError as e:
                detail = e.read()[:500].decode("utf-8", "replace")
                last = f"HTTP {e.code}: {detail}"
                if e.code not in RETRY_STATUS:
                    raise ModelError(last) from None
                ra = e.headers.get("Retry-After") if e.headers else None
                if ra and ra.isdigit():
                    delay = min(60.0, float(ra))
            except (urllib.error.URLError, socket.timeout, TimeoutError, ConnectionError) as e:
                last = f"connection: {getattr(e, 'reason', e)}"
            except ValueError as e:
                last = f"bad response: {e}"
            if attempt < self.cfg.max_retries:
                self._sleep(delay)
        raise ModelError(f"model request failed after {self.cfg.max_retries + 1} attempts: {last}")

    @staticmethod
    def _parse(data: dict) -> Reply:
        try:
            choice = data["choices"][0]
            msg = choice.get("message") or {}
        except (KeyError, IndexError, TypeError):
            raise ModelError(f"no choices in response: {str(data)[:300]}") from None
        content = msg.get("content") or ""
        if isinstance(content, list):  # content parts
            content = "".join(p.get("text", "") for p in content if isinstance(p, dict))
        calls = []
        for c in msg.get("tool_calls") or []:
            fn = c.get("function") or {}
            args = fn.get("arguments", "{}")
            if not isinstance(args, str):
                args = json.dumps(args)
            calls.append(ToolCall(c.get("id") or _new_id(), fn.get("name", ""), args or "{}"))
        if not calls and "<tool_call>" in content:
            content, calls = parse_text_tool_calls(content)
        return Reply(content=content, tool_calls=calls,
                     finish_reason=choice.get("finish_reason") or "",
                     usage=data.get("usage") or {}, raw=data)
