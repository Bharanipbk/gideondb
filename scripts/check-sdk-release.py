#!/usr/bin/env python3
"""Validate coordinated stable SDK versions; optionally require a release tag."""
import argparse
import json
from pathlib import Path
import re
import tomllib
import xml.etree.ElementTree as ET

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--tag')
args = parser.parse_args()
versions = {
    'npm': json.loads((root / 'sdk/typescript/package.json').read_text())['version'],
    'pypi': tomllib.loads((root / 'sdk/python/pyproject.toml').read_text())['project']['version'],
    'crates': tomllib.loads((root / 'sdk/rust/Cargo.toml').read_text())['package']['version'],
    'nuget': ET.parse(root / 'sdk/dotnet/src/GideonDB.Client/GideonDB.Client.csproj').findtext('.//Version'),
    'maven': ET.parse(root / 'sdk/java/pom.xml').findtext('{http://maven.apache.org/POM/4.0.0}version'),
}
if len(set(versions.values())) != 1:
    raise SystemExit(f'SDK versions disagree: {versions}')
version = versions['npm']
if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version) or version == '0.0.0':
    raise SystemExit(f'Expected a non-placeholder stable version: {version}')
if args.tag is not None and args.tag != f'v{version}':
    raise SystemExit(f'Release must run from tag v{version}, got {args.tag!r}')
print(f'All SDK package versions: {version}')
