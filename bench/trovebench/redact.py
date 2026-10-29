"""Scrub credentials from any text before it is logged or written.

Mirrors the rule in the Trove brief: API keys are never logged, stored or printed.
Two layers: exact values of secrets we know about (the environment keys we pass
in), and patterns for common key shapes in case one leaks in from elsewhere.
"""

from __future__ import annotations

import re
from typing import Iterable

PLACEHOLDER = "[REDACTED]"

_PATTERNS = [
    re.compile(r"sk-ant-[A-Za-z0-9_\-]{8,}"),
    re.compile(r"sk-(?:proj-)?[A-Za-z0-9_\-]{16,}"),
    re.compile(r"gh[pousr]_[A-Za-z0-9]{20,}"),
    re.compile(r"github_pat_[A-Za-z0-9_]{20,}"),
    re.compile(r"(?i)(x-api-key|authorization|proxy-authorization)(\s*[:=]\s*)(bearer\s+)?[^\s\"',;]+"),
    re.compile(r"(?i)\b(ANTHROPIC_API_KEY|OPENAI_API_KEY|CODEX_API_KEY|GITHUB_TOKEN)(\s*=\s*)[^\s\"',;]+"),
]


def redact(text: str, secrets: Iterable[str] = ()) -> str:
    for s in secrets:
        if s and len(s) >= 8:
            text = text.replace(s, PLACEHOLDER)
    for pat in _PATTERNS:
        if pat.groups >= 2:
            text = pat.sub(lambda m: f"{m.group(1)}{m.group(2)}{PLACEHOLDER}", text)
        else:
            text = pat.sub(PLACEHOLDER, text)
    return text
