#!/usr/bin/env python3
"""Validate a private provider registry and its capabilities handshake."""

from __future__ import annotations

import argparse
import json
import os
import stat
import subprocess
import uuid
from pathlib import Path


PROTOCOL = "issue-spec.code-provider/v1"
ALLOWED_CAPABILITIES = {"evidence.snapshot", "change.create", "change.comment"}


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def load_strict_json(raw: str):
    return json.loads(raw, object_pairs_hook=strict_object)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry", required=True, type=Path)
    parser.add_argument("--provider-key", required=True)
    return parser.parse_args()


def fail(message: str) -> None:
    raise SystemExit(f"provider validation failed: {message}")


def main() -> None:
    args = parse_args()
    registry_path = args.registry.expanduser().resolve()
    try:
        info = registry_path.lstat()
    except OSError:
        fail("registry is unavailable")
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode):
        fail("registry must be a non-symlink regular file")
    if os.name != "nt" and info.st_mode & 0o077:
        fail("registry must use mode 0600 or stricter")
    if info.st_size > 1024 * 1024:
        fail("registry exceeds 1 MiB")

    try:
        registry = load_strict_json(registry_path.read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError) as error:
        fail(f"registry JSON is invalid: {error}")
    if set(registry) != {"version", "providers"} or registry["version"] != 1:
        fail("registry must use the strict version 1 shape")
    providers = registry.get("providers")
    if not isinstance(providers, dict) or args.provider_key not in providers:
        fail("provider key is not registered")
    entry = providers[args.provider_key]
    allowed_entry = {"path", "args", "environment", "timeout", "max_output_bytes", "description"}
    if not isinstance(entry, dict) or "path" not in entry or not set(entry).issubset(allowed_entry):
        fail("provider registration shape is invalid")

    command_path = Path(entry["path"])
    if not command_path.is_absolute() or not command_path.is_file() or not os.access(command_path, os.X_OK):
        fail("provider path must be an executable absolute regular file")
    command = [str(command_path), *entry.get("args", [])]
    environment = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
    for value in entry.get("environment", []):
        if not isinstance(value, str) or "=" not in value:
            fail("operator environment entry is invalid")
        name, content = value.split("=", 1)
        if not name or name in environment:
            fail("operator environment entry is invalid or duplicated")
        environment[name] = content

    request_id = str(uuid.uuid4())
    request = {"protocol": PROTOCOL, "request_id": request_id, "action": "capabilities", "payload": None}
    try:
        completed = subprocess.run(
            command,
            input=json.dumps(request) + "\n",
            text=True,
            capture_output=True,
            env=environment,
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        fail("capabilities command could not complete")
    if completed.returncode != 0:
        fail("capabilities command returned a non-zero exit code")
    if len(completed.stdout.encode()) > 4 * 1024 * 1024 or len(completed.stderr.encode()) > 4 * 1024 * 1024:
        fail("provider output exceeds the maximum bound")
    try:
        response = load_strict_json(completed.stdout)
    except (ValueError, json.JSONDecodeError):
        fail("capabilities response is not one strict JSON object")
    if set(response) != {"protocol", "request_id", "capabilities"}:
        fail("capabilities response has unexpected fields")
    if response["protocol"] != PROTOCOL or response["request_id"] != request_id:
        fail("capabilities response identity does not match")
    capabilities = response["capabilities"]
    if not isinstance(capabilities, dict) or set(capabilities) != {"protocol_version", "values"}:
        fail("capabilities payload shape is invalid")
    values = capabilities["values"]
    if capabilities["protocol_version"] != PROTOCOL or not isinstance(values, list):
        fail("capabilities protocol is invalid")
    if len(values) != len(set(values)) or not set(values).issubset(ALLOWED_CAPABILITIES):
        fail("capabilities contain duplicates or unsupported values")
    described = entry.get("description", {}).get("capabilities", [])
    if sorted(values) != sorted(described):
        fail("runtime capabilities do not match operator description")

    print(json.dumps({"ok": True, "provider_key": args.provider_key, "capabilities": sorted(values)}, indent=2))


if __name__ == "__main__":
    main()
