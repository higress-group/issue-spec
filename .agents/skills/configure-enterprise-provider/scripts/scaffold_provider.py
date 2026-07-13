#!/usr/bin/env python3
"""Create a provider-neutral issue-spec code bridge scaffold."""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import stat
from pathlib import Path


PROVIDER_KEY = re.compile(r"^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$")
HOST_LABEL = re.compile(r"^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$")
CAPABILITIES = {"evidence.snapshot", "change.create", "change.comment"}
EVIDENCE = {"change", "review", "check", "merge", "archive"}


BRIDGE_TEMPLATE = r'''#!/usr/bin/env python3
"""Provider wrapper scaffold for issue-spec.code-provider/v1.

Replace the not_implemented branches with calls to the company code platform.
Keep credentials in the operator environment or token file, never in responses.
"""

from __future__ import annotations

import json
import sys


PROTOCOL = "issue-spec.code-provider/v1"
PROVIDER_KEY = __PROVIDER_KEY__
CAPABILITIES = __CAPABILITIES__


class BridgeError(Exception):
    def __init__(self, code: str, message: str) -> None:
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


def require_keys(value, required, optional=()):
    if not isinstance(value, dict):
        raise BridgeError("invalid_request", "request must be an object")
    keys = set(value)
    allowed = set(required) | set(optional)
    if not set(required).issubset(keys) or not keys.issubset(allowed):
        raise BridgeError("invalid_request", "request shape is invalid")


def snapshot(payload):
    require_keys(payload, {"reference", "subject_revision"})
    reference = payload["reference"]
    require_keys(reference, {"provider_key", "external_repository", "change_id"})
    if reference["provider_key"] != PROVIDER_KEY:
        raise BridgeError("reference_mismatch", "provider reference does not match")

    # TODO: Fetch the change, reviews, checks, and merge state for exactly
    # payload["subject_revision"]. Return stable facts; never infer approval.
    raise BridgeError("not_implemented", "snapshot mapping is not implemented")


def mutate(payload):
    require_keys(
        payload,
        {"kind", "reference"},
        {"title", "body", "base_revision", "head_revision", "metadata"},
    )
    reference = payload["reference"]
    require_keys(reference, {"provider_key", "external_repository", "change_id"})
    if reference["provider_key"] != PROVIDER_KEY:
        raise BridgeError("reference_mismatch", "provider reference does not match")

    # TODO: Implement create_change and/or comment only when advertised.
    # Echo the exact reference for comments; only create_change may assign a
    # new change_id. Return a canonical HTTPS URL without credentials.
    raise BridgeError("not_implemented", "mutation mapping is not implemented")


def handle(request):
    require_keys(request, {"protocol", "request_id", "action"}, {"payload"})
    if request["protocol"] != PROTOCOL or not request["request_id"]:
        raise BridgeError("invalid_request", "protocol or request identity is invalid")

    action = request["action"]
    if action == "capabilities":
        if request.get("payload") is not None:
            raise BridgeError("invalid_request", "capabilities payload must be null")
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "capabilities": {
                "protocol_version": PROTOCOL,
                "values": CAPABILITIES,
            },
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
    parser.add_argument("--capability", action="append", choices=sorted(CAPABILITIES), default=[])
    parser.add_argument("--recommended-evidence", action="append", choices=sorted(EVIDENCE), default=[])
    parser.add_argument("--code-change-label", default="Merge request")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--force", action="store_true")
    return parser.parse_args()


def valid_authority(value: str) -> bool:
    host = value
    if ":" in value:
        if value.count(":") != 1:
            return False
        host, port = value.rsplit(":", 1)
        if not port.isdigit() or not 1 <= int(port) <= 65535:
            return False
    if not host or len(host) > 253:
        return False
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return all(HOST_LABEL.fullmatch(label) for label in host.split("."))


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
    args.recommended_evidence = sorted(set(args.recommended_evidence))
    if args.recommended_evidence and "evidence.snapshot" not in args.capability:
        raise SystemExit("recommended evidence requires --capability evidence.snapshot")


def write_file(path: Path, content: str, mode: int, force: bool) -> None:
    if path.exists() and not force:
        raise SystemExit(f"refusing to overwrite {path}")
    path.write_text(content, encoding="utf-8")
    os.chmod(path, mode)


def main() -> None:
    args = parse_args()
    validate(args)
    output = args.output.expanduser().resolve()
    if output.exists() and not output.is_dir():
        raise SystemExit(f"output is not a directory: {output}")
    output.mkdir(mode=0o700, parents=True, exist_ok=True)

    bridge = BRIDGE_TEMPLATE.replace("__PROVIDER_KEY__", repr(args.provider_key)).replace(
        "__CAPABILITIES__", repr(args.capability)
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
                    "capabilities": args.capability,
                    "recommended_evidence": args.recommended_evidence,
                },
            }
        },
    }
    registry_path = output / "providers.json"
    write_file(registry_path, json.dumps(registry, indent=2) + "\n", 0o600, args.force)

    print(
        json.dumps(
            {
                "ok": True,
                "provider_key": args.provider_key,
                "capabilities": args.capability,
                "files": [bridge_path.name, registry_path.name],
                "next": "implement platform mappings, then run validate_provider.py",
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
