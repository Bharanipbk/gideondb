#!/usr/bin/env python3
"""Dependency-free structural checks for the published OpenAPI document."""

from pathlib import Path
import re
import sys

OPENAPI = Path(__file__).resolve().parents[1] / "docs/api/openapi.yaml"
text = OPENAPI.read_text(encoding="utf-8")


def fail(message: str) -> None:
    print(f"OpenAPI contract: {message}", file=sys.stderr)
    raise SystemExit(1)


if not re.search(r"(?m)^openapi:\s+3\.", text):
    fail("must declare OpenAPI 3.x")
if not re.search(r"(?m)^\s*title:\s+GideonDB", text):
    fail("must identify GideonDB")
if "bearerAuth:" not in text or "type: http" not in text or "scheme: bearer" not in text:
    fail("must define bearer authentication")

paths = re.findall(r"(?m)^  (/[^:]+):\s*$", text)
if len(paths) != len(set(paths)):
    fail("contains duplicate path declarations")
if len(paths) < 10:
    fail("publishes fewer than 10 API paths")

operation_ids = re.findall(r"(?m)^\s+operationId:\s*([A-Za-z0-9_.-]+)\s*$", text)
if not operation_ids or len(operation_ids) != len(set(operation_ids)):
    fail("operationId values must be present and unique")

required = {"/health", "/ready", "/collections", "/collections/{name}", "/collections/{name}/search"}
missing = sorted(required - set(paths))
if missing:
    fail("missing required paths: " + ", ".join(missing))

print(f"OpenAPI contract passed: {len(paths)} paths, {len(operation_ids)} operations")
