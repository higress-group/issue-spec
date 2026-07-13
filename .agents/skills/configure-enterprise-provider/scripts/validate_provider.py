#!/usr/bin/env python3
"""Validate a private provider registry and its capabilities handshake."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import stat
import subprocess
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


def private_registry(info: os.stat_result) -> bool:
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_nlink != 1:
        return False
    return os.name == "nt" or info.st_mode & 0o077 == 0


def read_registry(raw_path: Path) -> tuple[Path, dict]:
    expanded = os.path.expanduser(str(raw_path))
    path = Path(expanded)
    if not path.is_absolute() or os.path.normpath(expanded) != expanded:
        fail("registry must use a clean absolute path")
    try:
        before = path.lstat()
    except OSError:
        fail("registry is unavailable")
    if not private_registry(before):
        fail("registry must be a private single-link non-symlink regular file")
    if before.st_size > MAXIMUM_REGISTRY_BYTES:
        fail("registry exceeds 1 MiB")

    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError:
        fail("registry changed or could not be opened safely")
    try:
        after = os.fstat(descriptor)
        if not private_registry(after) or not os.path.samestat(before, after):
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
        decoded = raw.decode("utf-8")
        registry = load_strict_json(decoded)
    except (UnicodeDecodeError, ValueError, json.JSONDecodeError) as error:
        fail(f"registry JSON is invalid: {error}")
    if not isinstance(registry, dict):
        fail("registry must be a JSON object")
    return path, registry


def valid_provider_key(value) -> bool:
    return isinstance(value, str) and len(value) <= 128 and PROVIDER_KEY.fullmatch(value) is not None


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


def valid_port(value: str) -> bool:
    return value.isdigit() and 1 <= int(value) <= 65535


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


def string_list(value, name: str, maximum: int | None = None) -> list[str]:
    if value is None:
        return []
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        fail(f"{name} must be a string array")
    if maximum is not None and len(value) > maximum:
        fail(f"{name} contains too many values")
    return value


def validate_description(value, key: str) -> list[str]:
    if value is None:
        value = {}
    allowed = {
        "provider_key",
        "display_name",
        "remote_authorities",
        "code_change_label",
        "capabilities",
        "recommended_evidence",
    }
    if not isinstance(value, dict) or not set(value).issubset(allowed):
        fail(f"provider {key} description shape is invalid")
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
    return capabilities


def validate_entry(key: str, entry) -> dict:
    allowed = {"path", "args", "environment", "timeout", "max_output_bytes", "description"}
    if not isinstance(entry, dict) or set(entry) - allowed or "path" not in entry:
        fail(f"provider {key} registration shape is invalid")
    command_value = entry["path"]
    if not isinstance(command_value, str) or not os.path.isabs(command_value) or os.path.normpath(command_value) != command_value:
        fail(f"provider {key} path must be clean and absolute")
    try:
        command_info = os.stat(command_value)
    except OSError:
        fail(f"provider {key} executable is unavailable")
    if not stat.S_ISREG(command_info.st_mode) or (os.name != "nt" and command_info.st_mode & 0o111 == 0):
        fail(f"provider {key} path must be an executable regular file")

    arguments = string_list(entry.get("args"), "provider args", 32)
    if any(len(item.encode("utf-8")) > 4096 or "\x00" in item for item in arguments):
        fail(f"provider {key} arguments are invalid")
    configured_environment = string_list(entry.get("environment"), "provider environment")
    environment = {"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "PATH": "/usr/bin:/bin"}
    seen_environment = set()
    for item in configured_environment:
        if "=" not in item:
            fail(f"provider {key} environment entry is invalid")
        name, content = item.split("=", 1)
        if not name or any(character in name for character in "\x00\r\n") or "\x00" in content or name in seen_environment:
            fail(f"provider {key} environment entry is invalid or duplicated")
        seen_environment.add(name)
        environment[name] = content

    timeout = parse_duration(entry.get("timeout"))
    output_limit = entry.get("max_output_bytes", 0)
    if output_limit == 0:
        output_limit = DEFAULT_OUTPUT_BYTES
    if isinstance(output_limit, bool) or not isinstance(output_limit, int) or not 1024 <= output_limit <= MAXIMUM_OUTPUT_BYTES:
        fail(f"provider {key} output bound must be between 1 KiB and 4 MiB")
    capabilities = validate_description(entry.get("description"), key)
    return {
        "command": [command_value, *arguments],
        "environment": environment,
        "timeout": timeout,
        "output_limit": output_limit,
        "capabilities": capabilities,
    }


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


def invoke_capabilities(config: dict, request: dict) -> str:
    try:
        process = subprocess.Popen(
            config["command"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=config["environment"],
        )
    except OSError:
        fail("capabilities command could not start")
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
            fail("capabilities command timed out")
        time.sleep(0.01)
    if exceeded.is_set() and process.poll() is None:
        process.kill()
    process.wait()
    stdout_thread.join()
    stderr_thread.join()
    if exceeded.is_set():
        fail("provider output exceeds its configured bound")
    if process.returncode != 0:
        fail("capabilities command returned a non-zero exit code")
    try:
        return captured.get("stdout", b"").decode("utf-8")
    except UnicodeDecodeError:
        fail("capabilities response is not UTF-8")


def main() -> None:
    args = parse_args()
    _, registry = read_registry(args.registry)
    if set(registry) != {"version", "providers"} or type(registry.get("version")) is not int or registry["version"] != 1:
        fail("registry must use the strict version 1 shape")
    providers = registry.get("providers")
    if not isinstance(providers, dict) or not 1 <= len(providers) <= 32:
        fail("registry must contain between 1 and 32 providers")
    validated = {}
    for key, entry in providers.items():
        if not valid_provider_key(key):
            fail("provider key is invalid")
        validated[key] = validate_entry(key, entry)
    if args.provider_key not in validated:
        fail("provider key is not registered")
    config = validated[args.provider_key]

    request_id = str(uuid.uuid4())
    request = {"protocol": PROTOCOL, "request_id": request_id, "action": "capabilities", "payload": None}
    raw_response = invoke_capabilities(config, request)
    try:
        response = load_strict_json(raw_response)
    except (ValueError, json.JSONDecodeError):
        fail("capabilities response is not one strict JSON object")
    if not isinstance(response, dict) or set(response) != {"protocol", "request_id", "capabilities"}:
        fail("capabilities response has unexpected fields")
    if response["protocol"] != PROTOCOL or response["request_id"] != request_id:
        fail("capabilities response identity does not match")
    capabilities = response["capabilities"]
    if not isinstance(capabilities, dict) or set(capabilities) != {"protocol_version", "values"}:
        fail("capabilities payload shape is invalid")
    values = capabilities["values"]
    if capabilities["protocol_version"] != PROTOCOL or not isinstance(values, list):
        fail("capabilities protocol is invalid")
    if any(not isinstance(value, str) for value in values) or len(values) != len(set(values)) or not set(values).issubset(ALLOWED_CAPABILITIES):
        fail("capabilities contain duplicates or unsupported values")
    if sorted(values) != sorted(config["capabilities"]):
        fail("runtime capabilities do not match operator description")

    print(json.dumps({"ok": True, "provider_key": args.provider_key, "capabilities": sorted(values)}, indent=2))


if __name__ == "__main__":
    main()
