#!/usr/bin/env python3
"""Validate an operator-owned issue-spec code-provider registry and handshake."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import stat
import subprocess
import sys
import threading
import time
import uuid
from pathlib import Path


PROTOCOL = "issue-spec.code-provider/v1"
PROVIDER_KEY = re.compile(r"^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$")
HOST_LABEL = re.compile(r"^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$")
DURATION_PART = re.compile(r"(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)")
ALLOWED_CAPABILITIES = {"evidence.snapshot", "change.create", "change.comment"}
ALLOWED_EVIDENCE = {"change", "review", "check", "merge", "archive"}
MAXIMUM_REGISTRY_BYTES = 1024 * 1024
DEFAULT_TIMEOUT_SECONDS = 30.0
MAXIMUM_TIMEOUT_SECONDS = 120.0
DEFAULT_OUTPUT_BYTES = 1024 * 1024
MAXIMUM_OUTPUT_BYTES = 4 * 1024 * 1024


class ProviderInvocationError(Exception):
    pass


def fail(message: str) -> None:
    raise SystemExit(message)


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def load_strict_json(raw: str):
    return json.loads(raw, object_pairs_hook=strict_object)


def private_registry(info: os.stat_result) -> bool:
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_nlink != 1:
        return False
    return os.name == "nt" or info.st_mode & 0o077 == 0


def read_registry(raw_path: Path) -> tuple[Path, dict]:
    expanded = os.path.expanduser(str(raw_path))
    path = Path(expanded)
    if not path.is_absolute() or os.path.normpath(expanded) != expanded:
        fail("--registry must be a clean absolute path")
    try:
        info = path.lstat()
    except OSError:
        fail("registry is unavailable")
    if not private_registry(info):
        fail("registry must be a private mode-0600-or-stricter single-link non-symlink regular file")
    if info.st_size > MAXIMUM_REGISTRY_BYTES:
        fail("registry exceeds 1 MiB")
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError:
        fail("registry changed or could not be opened safely")
    try:
        after = os.fstat(descriptor)
        if not private_registry(after) or not os.path.samestat(info, after):
            fail("registry changed while opening")
        with os.fdopen(descriptor, "rb", closefd=True) as source:
            descriptor = -1
            raw = source.read(MAXIMUM_REGISTRY_BYTES + 1)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
    if len(raw) > MAXIMUM_REGISTRY_BYTES:
        fail("registry exceeds 1 MiB")
    try:
        payload = load_strict_json(raw.decode("utf-8"))
    except (UnicodeDecodeError, ValueError, json.JSONDecodeError):
        fail("registry is not strict JSON")
    if not isinstance(payload, dict) or set(payload) != {"version", "providers"}:
        fail("registry shape is invalid")
    if type(payload["version"]) is not int or payload["version"] != 1 or not isinstance(payload["providers"], dict):
        fail("registry version or providers is invalid")
    return path, payload


def string_list(value, field: str, maximum: int | None = None) -> list[str]:
    if value is None:
        return []
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        fail(f"{field} must be an array of strings")
    if maximum is not None and len(value) > maximum:
        fail(f"{field} contains too many values")
    return value


def valid_port(value: str) -> bool:
    return value.isdigit() and 1 <= int(value) <= 65535


def valid_authority(value: str) -> bool:
    if not isinstance(value, str) or not value or len(value) > 253 or any(
        character in value for character in "/@?#\\\r\n\t "
    ):
        return False
    host = value
    if value.startswith("["):
        closing = value.find("]")
        if closing < 0:
            return False
        host = value[1:closing]
        suffix = value[closing + 1 :]
        if suffix and (not suffix.startswith(":") or not valid_port(suffix[1:])):
            return False
    elif value.count(":") == 1:
        candidate, port = value.rsplit(":", 1)
        if valid_port(port):
            host = candidate
    elif value.count(":") > 1:
        host = value
    if not host:
        return False
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return all(len(label) <= 63 and HOST_LABEL.fullmatch(label) for label in host.split("."))


def parse_duration(value) -> float:
    if value in (None, ""):
        return DEFAULT_TIMEOUT_SECONDS
    if not isinstance(value, str):
        fail("provider timeout must be a duration string")
    if value.startswith("+"):
        value = value[1:]
    if not value or value.startswith("-"):
        fail("provider timeout is invalid")
    units = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1.0, "m": 60.0, "h": 3600.0}
    position = 0
    total = 0.0
    while position < len(value):
        match = DURATION_PART.match(value, position)
        if match is None:
            fail("provider timeout is invalid")
        total += float(match.group(1)) * units[match.group(2)]
        position = match.end()
    if total <= 0 or total > MAXIMUM_TIMEOUT_SECONDS:
        fail("provider timeout must be greater than zero and at most two minutes")
    return total


def validate_description(key: str, value) -> list[str]:
    allowed = {
        "provider_key", "display_name", "remote_authorities", "code_change_label",
        "capabilities", "recommended_evidence",
    }
    if value is None:
        value = {}
    if not isinstance(value, dict) or not set(value).issubset(allowed):
        fail(f"provider {key} description has unsupported fields")
    described_key = value.get("provider_key", key)
    if described_key in (None, ""):
        described_key = key
    if described_key != key:
        fail(f"provider {key} description key does not match registration")
    for field in ("display_name", "code_change_label"):
        content = value.get(field, "")
        if not isinstance(content, str) or len(content.strip()) > 128:
            fail(f"provider {key} {field} is invalid")
    authorities = [item.strip().lower() for item in string_list(value.get("remote_authorities"), "remote_authorities")]
    if len(authorities) != len(set(authorities)) or any(not valid_authority(item) for item in authorities):
        fail(f"provider {key} remote authorities are invalid")
    capabilities = string_list(value.get("capabilities"), "capabilities")
    if len(capabilities) != len(set(capabilities)) or not set(capabilities).issubset(ALLOWED_CAPABILITIES):
        fail(f"provider {key} capabilities are invalid")
    evidence = string_list(value.get("recommended_evidence"), "recommended_evidence")
    if len(evidence) != len(set(evidence)) or not set(evidence).issubset(ALLOWED_EVIDENCE):
        fail(f"provider {key} recommended evidence is invalid")
    return sorted(capabilities)


def validate_entry(key: str, value) -> dict:
    allowed = {"path", "args", "environment", "timeout", "max_output_bytes", "description"}
    if not isinstance(value, dict) or not set(value).issubset(allowed) or "path" not in value:
        fail(f"provider {key} registration has unsupported fields")
    path_value = value.get("path")
    if not isinstance(path_value, str) or not os.path.isabs(path_value) or os.path.normpath(path_value) != path_value:
        fail(f"provider {key} path must be clean and absolute")
    path = Path(path_value)
    try:
        info = path.stat()
    except OSError:
        fail(f"provider {key} executable is unavailable")
    if not stat.S_ISREG(info.st_mode) or (os.name != "nt" and info.st_mode & 0o111 == 0):
        fail(f"provider {key} path must be an executable regular file")
    args = string_list(value.get("args"), "args", 32)
    if any(len(item) > 4096 or "\x00" in item for item in args):
        fail(f"provider {key} arguments are invalid")
    configured_environment = string_list(value.get("environment"), "environment")
    environment = {"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "PATH": "/usr/bin:/bin"}
    names = []
    for item in configured_environment:
        name, separator, content = item.partition("=")
        if not separator or not name or any(character in name for character in "\x00\r\n") or "\x00" in content or name in names:
            fail(f"provider {key} environment is invalid")
        names.append(name)
        environment[name] = content
    timeout = parse_duration(value.get("timeout"))
    max_output = value.get("max_output_bytes", 0)
    if max_output == 0:
        max_output = DEFAULT_OUTPUT_BYTES
    if isinstance(max_output, bool) or not isinstance(max_output, int) or not 1024 <= max_output <= MAXIMUM_OUTPUT_BYTES:
        fail(f"provider {key} max_output_bytes is invalid")
    capabilities = validate_description(key, value.get("description"))
    return {"command": [str(path), *args], "environment": environment, "timeout": timeout,
            "output_limit": max_output, "capabilities": capabilities}


def read_bounded(stream, limit: int, output: dict, name: str, exceeded: threading.Event) -> None:
    chunks = []
    total = 0
    try:
        while True:
            chunk = stream.read(min(65536, limit + 1 - total))
            if not chunk:
                break
            chunks.append(chunk)
            total += len(chunk)
            if total > limit:
                exceeded.set()
                break
    finally:
        stream.close()
    output[name] = b"".join(chunks)


def invoke_provider(config: dict, request: dict) -> str:
    try:
        process = subprocess.Popen(
            config["command"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, env=config["environment"],
        )
    except OSError:
        raise ProviderInvocationError("capabilities command could not start")
    captured = {}
    exceeded = threading.Event()
    stdout_thread = threading.Thread(
        target=read_bounded,
        args=(process.stdout, config["output_limit"], captured, "stdout", exceeded),
        daemon=True,
    )
    stderr_thread = threading.Thread(
        target=read_bounded,
        args=(process.stderr, config["output_limit"], captured, "stderr", exceeded),
        daemon=True,
    )
    stdout_thread.start()
    stderr_thread.start()
    try:
        process.stdin.write((json.dumps(request) + "\n").encode("utf-8"))
        process.stdin.close()
    except OSError:
        process.kill()
    deadline = time.monotonic() + config["timeout"]
    while process.poll() is None and not exceeded.is_set():
        if time.monotonic() >= deadline:
            process.kill()
            process.wait()
            stdout_thread.join()
            stderr_thread.join()
            raise ProviderInvocationError("capabilities command timed out")
        time.sleep(0.01)
    if exceeded.is_set() and process.poll() is None:
        process.kill()
    process.wait()
    stdout_thread.join()
    stderr_thread.join()
    if exceeded.is_set():
        raise ProviderInvocationError("capabilities output exceeded its configured bound")
    if process.returncode != 0:
        raise ProviderInvocationError("capabilities command returned a non-zero exit code")
    try:
        return captured.get("stdout", b"").decode("utf-8")
    except UnicodeDecodeError:
        raise ProviderInvocationError("capabilities response is not UTF-8")


def invoke_capabilities(config: dict):
    request_id = str(uuid.uuid4())
    request = {"protocol": PROTOCOL, "request_id": request_id, "action": "capabilities"}
    try:
        raw_response = invoke_provider(config, request)
    except ProviderInvocationError as error:
        fail(str(error))
    try:
        response = load_strict_json(raw_response)
    except (ValueError, json.JSONDecodeError):
        fail("provider capabilities response is not one strict JSON object")
    if not isinstance(response, dict) or set(response) != {"protocol", "request_id", "capabilities"}:
        fail("provider capabilities response shape is invalid")
    if response["protocol"] != PROTOCOL or response["request_id"] != request_id:
        fail("provider capabilities response identity does not match request")
    capabilities = response["capabilities"]
    if not isinstance(capabilities, dict) or set(capabilities) != {"protocol_version", "values"}:
        fail("provider capabilities payload shape is invalid")
    if capabilities["protocol_version"] != PROTOCOL:
        fail("provider capabilities protocol version is invalid")
    values = string_list(capabilities["values"], "runtime capabilities")
    if len(values) != len(set(values)) or not set(values).issubset(ALLOWED_CAPABILITIES):
        fail("provider runtime capabilities are invalid")
    return sorted(values)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry", required=True, type=Path)
    parser.add_argument("--provider-key", required=True)
    args = parser.parse_args()
    if len(args.provider_key) > 128 or not PROVIDER_KEY.fullmatch(args.provider_key):
        fail("--provider-key is invalid")
    registry_path, registry = read_registry(args.registry)
    providers = registry["providers"]
    if not 1 <= len(providers) <= 32:
        fail("registry must contain between 1 and 32 providers")
    validated = {}
    for key, entry in providers.items():
        if not isinstance(key, str) or len(key) > 128 or not PROVIDER_KEY.fullmatch(key):
            fail("provider key is invalid")
        validated[key] = validate_entry(key, entry)
    if args.provider_key not in validated:
        fail(f"provider {args.provider_key} is not registered")
    config = validated[args.provider_key]
    runtime = invoke_capabilities(config)
    if runtime != config["capabilities"]:
        fail("provider runtime capabilities do not match operator description")
    print(json.dumps({
        "ok": True,
        "registry": str(registry_path),
        "provider_key": args.provider_key,
        "capabilities": runtime,
        "operations": {
            "create_change": "change.create" in runtime,
            "comment": "change.comment" in runtime,
            "snapshot": "evidence.snapshot" in runtime,
        },
        "note": "Handshake validation does not replace non-production operation contract tests.",
    }, indent=2))


if __name__ == "__main__":
    main()
