#!/usr/bin/env python3
"""Validate repository-local Markdown links without network access."""

from pathlib import Path
import re
import sys
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parents[1]
SKIP = {".git", "target", "node_modules"}
LINK = re.compile(r"(?<!!)\[[^]]*\]\(([^)]+)\)")


def anchors(text: str) -> set[str]:
    result: set[str] = set()
    for heading in re.findall(r"^#{1,6}\s+(.+?)\s*#*\s*$", text, re.MULTILINE):
        value = re.sub(r"[^a-z0-9 _-]", "", heading.lower())
        result.add(re.sub(r"[ _]+", "-", value).strip("-"))
    return result


errors: list[str] = []
for document in ROOT.rglob("*.md"):
    if any(part in SKIP or part.startswith(".") for part in document.relative_to(ROOT).parts):
        continue
    text = document.read_text(encoding="utf-8")
    for raw in LINK.findall(text):
        target = raw.strip().split(maxsplit=1)[0].strip("<>")
        if not target or target.startswith(("http://", "https://", "mailto:")):
            continue
        path_text, _, fragment = target.partition("#")
        destination = document if not path_text else document.parent / unquote(path_text)
        if not destination.exists():
            errors.append(f"{document.relative_to(ROOT)}: missing {target}")
            continue
        if fragment and destination.suffix.lower() == ".md":
            if unquote(fragment).lower() not in anchors(destination.read_text(encoding="utf-8")):
                errors.append(f"{document.relative_to(ROOT)}: missing anchor {target}")

if errors:
    print("\n".join(errors), file=sys.stderr)
    raise SystemExit(1)
print("Markdown links passed")
