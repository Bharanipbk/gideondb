#!/usr/bin/env python3
"""Dependency-free structural gate for the public protobuf contract."""

from pathlib import Path
import re
import sys

PROTO = Path(__file__).resolve().parents[1] / "api/proto/gideondb/v1/gideondb.proto"
REQUIRED_RPCS = {
    "CreateCollection", "DeleteCollection", "ListCollections", "DescribeCollection",
    "Upsert", "BatchUpsert", "Delete", "Get", "Search", "BatchSearch", "Scroll",
    "ClusterStatus", "ListNodes", "ListShards", "Health", "Stats", "Snapshot", "Restore",
}


def fail(message: str) -> None:
    print(f"protobuf contract: {message}", file=sys.stderr)
    raise SystemExit(1)


text = PROTO.read_text(encoding="utf-8")
if 'syntax = "proto3";' not in text or "package gideondb.v1;" not in text:
    fail("must use proto3 package gideondb.v1")
if "service GideonDBService" not in text:
    fail("must define GideonDBService")

rpcs = set(re.findall(r"^\s*rpc\s+(\w+)\s*\(", text, re.MULTILINE))
if missing := sorted(REQUIRED_RPCS - rpcs):
    fail("missing RPCs: " + ", ".join(missing))
if extra := sorted(rpcs - REQUIRED_RPCS):
    fail("unexpected undocumented RPCs: " + ", ".join(extra))

lines = text.splitlines()
declaration = re.compile(r"^\s*(?:service|rpc|message|enum)\s+\w+")
for index, line in enumerate(lines):
    if not declaration.match(line):
        continue
    previous = index - 1
    while previous >= 0 and not lines[previous].strip():
        previous -= 1
    if previous < 0 or not lines[previous].lstrip().startswith("//"):
        fail(f"line {index + 1} declaration lacks a preceding comment")

field = re.compile(r"^\s*(?:repeated\s+)?(?:[.\w<>]+)\s+\w+\s*=\s*\d+;")
for index, line in enumerate(lines):
    if not field.match(line):
        continue
    previous = index - 1
    while previous >= 0 and not lines[previous].strip():
        previous -= 1
    if previous < 0 or not lines[previous].lstrip().startswith("//"):
        fail(f"line {index + 1} field lacks a preceding comment")

print(f"protobuf contract passed: {len(rpcs)} documented RPCs")
