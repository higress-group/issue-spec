#!/usr/bin/env python3
"""Create a least-privilege issue-spec.code-provider/v1 bridge scaffold."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import stat
import tempfile
from pathlib import Path


PROVIDER_KEY = re.compile(r"^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$")
HOST_LABEL = re.compile(r"^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$")
CAPABILITIES = {"evidence.snapshot", "change.create", "change.comment"}


BRIDGE_TEMPLATE = r'''#!/usr/bin/env python3
"""Operator-owned bridge for issue-spec.code-provider/v1.

Implement only the operations listed in PLANNED_CAPABILITIES. Keep credentials
in the operator environment or token file and return no secret-bearing output.
"""

import json
import sys


PROTOCOL = "issue-spec.code-provider/v1"
PROVIDER_KEY = __PROVIDER_KEY__
PLANNED_CAPABILITIES = __PLANNED_CAPABILITIES__
# Move an operation here only after its handler and unhappy paths are tested.
CAPABILITIES = []


class BridgeError(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code
        self.message = message


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise BridgeError("invalid_request", "duplicate JSON field")
        result[key] = value
    return result


def require_object(value, required, optional=()):
    if not isinstance(value, dict):
        raise BridgeError("invalid_request", "request must be an object")
    keys = set(value)
    allowed = set(required) | set(optional)
    if not set(required).issubset(keys) or not keys.issubset(allowed):
        raise BridgeError("invalid_request", "request shape is invalid")


def require_text(value, message):
    if not isinstance(value, str) or not value.strip() or value != value.strip():
        raise BridgeError("invalid_request", message)


def validate_reference(reference, allow_empty_change=False):
    require_object(reference, {"provider_key", "external_repository", "change_id"})
    if reference["provider_key"] != PROVIDER_KEY:
        raise BridgeError("reference_mismatch", "provider reference does not match")
    require_text(reference["external_repository"], "external repository is invalid")
    if allow_empty_change:
        if reference["change_id"] != "":
            raise BridgeError("invalid_request", "create_change requires an empty change identity")
    else:
        require_text(reference["change_id"], "change identity is invalid")


def snapshot(payload):
    require_object(payload, {"reference", "subject_revision"})
    validate_reference(payload["reference"])
    require_text(payload["subject_revision"], "subject revision is invalid")
    # TODO: Return one exact-head provider fact snapshot for audit/navigation.
    raise BridgeError("not_implemented", "snapshot mapping is not implemented")


def mutate(payload):
    require_object(
        payload,
        {"kind", "reference"},
        {"title", "body", "base_revision", "head_revision", "metadata"},
    )
    kind = payload["kind"]
    if kind == "create_change":
        validate_reference(payload["reference"], allow_empty_change=True)
        require_text(payload.get("title"), "change title is invalid")
        require_text(payload.get("head_revision"), "head revision is invalid")
        # TODO: Create a provider-native PR/MR and return reference, canonical_url,
        # and external_id for the exact pushed head.
    elif kind == "comment":
        validate_reference(payload["reference"])
        require_text(payload.get("body"), "comment body is invalid")
        require_text(payload.get("head_revision"), "head revision is invalid")
        # TODO: Add a non-blocking provider-native discussion and echo the same
        # reference, canonical_url, and external_id.
    else:
        raise BridgeError("unsupported_action", "mutation kind is not supported")
    raise BridgeError("not_implemented", "mutation mapping is not implemented")


def handle(request):
    require_object(request, {"protocol", "request_id", "action"}, {"payload"})
    if request["protocol"] != PROTOCOL:
        raise BridgeError("invalid_request", "protocol is invalid")
    require_text(request["request_id"], "request identity is invalid")
    action = request["action"]
    if action == "capabilities":
        if request.get("payload") is not None:
            raise BridgeError("invalid_request", "capabilities payload must be null")
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "capabilities": {"protocol_version": PROTOCOL, "values": CAPABILITIES},
        }
    if action == "snapshot":
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "snapshot": snapshot(request.get("payload")),
        }
    if action == "mutate":
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "mutation": mutate(request.get("payload")),
        }
    raise BridgeError("unsupported_action", "action is not supported")


def main():
    request_id = "unknown"
    try:
        raw = sys.stdin.buffer.read(1024 * 1024 + 1)
        if len(raw) > 1024 * 1024:
            raise BridgeError("invalid_request", "request exceeds size limit")
        request = json.loads(raw, object_pairs_hook=strict_object)
        if isinstance(request, dict):
            request_id = str(request.get("request_id") or "unknown")
        response = handle(request)
    except BridgeError as error:
        response = {
            "protocol": PROTOCOL,
            "request_id": request_id,
            "error": {"code": error.code, "message": error.message},
        }
    except Exception:
        response = {
            "protocol": PROTOCOL,
            "request_id": request_id,
            "error": {"code": "internal_error", "message": "provider bridge failed"},
        }
    sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    main()
'''


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--provider-key", required=True)
    parser.add_argument("--display-name", required=True)
    parser.add_argument("--remote-authority", action="append", required=True)
    parser.add_argument("--capability", action="append", choices=sorted(CAPABILITIES), required=True)
    parser.add_argument("--code-change-label", default="Merge request")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--force", action="store_true")
    return parser.parse_args()


def valid_authority(value: str) -> bool:
    if not value or len(value) > 253 or any(character in value for character in "/@?#\\\r\n\t "):
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
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return bool(host) and all(
            len(label) <= 63 and HOST_LABEL.fullmatch(label) for label in host.split(".")
        )


def valid_port(value: str) -> bool:
    return value.isdigit() and 1 <= int(value) <= 65535


def validate(args: argparse.Namespace) -> None:
    if len(args.provider_key) > 128 or not PROVIDER_KEY.fullmatch(args.provider_key):
        raise SystemExit("--provider-key must be a lowercase operator registration key")
    if not args.display_name.strip() or len(args.display_name.strip()) > 128:
        raise SystemExit("--display-name must contain at most 128 characters")
    if not args.code_change_label.strip() or len(args.code_change_label.strip()) > 128:
        raise SystemExit("--code-change-label must contain at most 128 characters")
    authorities = [value.strip().lower() for value in args.remote_authority]
    if len(set(authorities)) != len(authorities) or any(not valid_authority(value) for value in authorities):
        raise SystemExit("--remote-authority values must be unique host[:port] authorities")
    args.remote_authority = authorities
    args.capability = sorted(set(args.capability))


def reject_symlink_components(path: Path) -> None:
    current = Path(path.anchor)
    for part in path.parts[1:]:
        current /= part
        try:
            info = current.lstat()
        except FileNotFoundError:
            return
        if stat.S_ISLNK(info.st_mode):
            raise SystemExit(f"output path contains a symbolic link: {current}")


def prepare_output_dir(raw: Path) -> Path:
    output = Path(os.path.expanduser(str(raw)))
    if not output.is_absolute() or os.path.normpath(str(output)) != str(output):
        raise SystemExit("--output must be a clean absolute path")
    reject_symlink_components(output)
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = output.lstat()
    if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise SystemExit(f"output is not a non-symlink directory: {output}")
    if os.name != "nt" and (info.st_mode & 0o077 or (hasattr(os, "getuid") and info.st_uid != os.getuid())):
        raise SystemExit("output directory must be private and owned by the current user")
    return output


def write_file(path: Path, content: str, mode: int, force: bool) -> None:
    try:
        existing = path.lstat()
    except FileNotFoundError:
        existing = None
    if existing is not None:
        if stat.S_ISLNK(existing.st_mode) or not stat.S_ISREG(existing.st_mode) or existing.st_nlink != 1:
            raise SystemExit(f"refusing unsafe pre-existing entry: {path}")
        if not force:
            raise SystemExit(f"refusing to overwrite {path}")
    encoded = content.encode("utf-8")
    if existing is None:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), mode)
        with os.fdopen(descriptor, "wb", closefd=True) as output:
            output.write(encoded)
        return
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        with os.fdopen(descriptor, "wb", closefd=True) as output:
            output.write(encoded)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> None:
    args = parse_args()
    validate(args)
    output = prepare_output_dir(args.output)
    bridge = BRIDGE_TEMPLATE.replace("__PROVIDER_KEY__", repr(args.provider_key)).replace(
        "__PLANNED_CAPABILITIES__", repr(args.capability)
    )
    bridge_path = output / "provider_bridge.py"
    write_file(bridge_path, bridge, 0o750, args.force)

    prefix = re.sub(r"[^A-Z0-9]", "_", args.provider_key.upper())
    registry = {
        "version": 1,
        "providers": {
            args.provider_key: {
                "path": str(bridge_path),
                "environment": [
                    f"{prefix}_API_URL=https://{args.remote_authority[0]}/api",
                    f"{prefix}_TOKEN_FILE=/run/secrets/{args.provider_key}-token",
                ],
                "timeout": "30s",
                "max_output_bytes": 1048576,
                "description": {
                    "display_name": args.display_name.strip(),
                    "remote_authorities": args.remote_authority,
                    "code_change_label": args.code_change_label.strip(),
                    "capabilities": [],
                    "recommended_evidence": [],
                },
            }
        },
    }
    registry_path = output / "providers.json"
    write_file(registry_path, json.dumps(registry, indent=2) + "\n", 0o600, args.force)

    activation = []
    if "change.create" in args.capability:
        activation.append("implement and contract-test create_change for one exact pushed head")
    if "change.comment" in args.capability:
        activation.append("implement and contract-test non-blocking provider-native comments")
    if "evidence.snapshot" in args.capability:
        activation.append("implement and contract-test exact-head audit snapshots")
    activation += [
        "move only implemented values into provider_bridge.py CAPABILITIES",
        "copy the same implemented values into providers.json description.capabilities",
        "run validate_provider.py and then exercise each operation in a non-production repository",
    ]
    plan = {
        "provider_key": args.provider_key,
        "planned_capabilities": args.capability,
        "activation": activation,
    }
    plan_path = output / "implementation-plan.json"
    write_file(plan_path, json.dumps(plan, indent=2) + "\n", 0o600, args.force)
    print(json.dumps({
        "ok": True,
        "provider_key": args.provider_key,
        "capabilities": [],
        "planned_capabilities": args.capability,
        "files": [bridge_path.name, registry_path.name, plan_path.name],
        "next": "implement only selected operations, then run validate_provider.py",
    }, indent=2))


if __name__ == "__main__":
    main()
