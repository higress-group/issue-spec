#!/usr/bin/env python3
"""Create a provider-neutral issue-spec code bridge scaffold."""

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
MERGE_AUTHORITY_CAPABILITIES = {
    "evidence.review-decision",
    "evidence.authoritative-check-conclusion",
    "change.merge-conditional",
}
OPTIONAL_CAPABILITIES = {"change.create", "change.comment"}
CAPABILITIES = MERGE_AUTHORITY_CAPABILITIES | OPTIONAL_CAPABILITIES
SEMANTIC_GENERATION = "minimal-merge-authority/v1"
MAXIMUM_MAPPING_BYTES = 1024 * 1024


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
SEMANTIC_GENERATION = "minimal-merge-authority/v1"
PROVIDER_BUILD_IDENTITY = __PROVIDER_BUILD_IDENTITY__
CONFORMANCE_PROTOCOL = "issue-spec.code-provider-conformance/v1"
CONFORMANCE_SENTINEL = "__issue_spec_conformance_probe__"
MERGE_AUTHORITY_CAPABILITIES = {
    "evidence.review-decision",
    "evidence.authoritative-check-conclusion",
    "change.merge-conditional",
}
# Keep the scaffold inert. Move values from PLANNED_CAPABILITIES into
# CAPABILITIES only after the corresponding handler and contract tests exist,
# then make the same change in providers.json.
PLANNED_CAPABILITIES = __PLANNED_CAPABILITIES__
CAPABILITIES = []


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


def require_text(value, message):
    if not isinstance(value, str) or not value.strip() or value != value.strip():
        raise BridgeError("invalid_request", message)


def validate_reference(reference):
    require_keys(reference, {"provider_key", "external_repository", "change_id"})
    if reference["provider_key"] != PROVIDER_KEY:
        raise BridgeError("reference_mismatch", "provider reference does not match")
    require_text(reference["external_repository"], "external repository is invalid")
    require_text(reference["change_id"], "change identity is invalid")


def validate_conformance_probe(payload, action):
    probe = payload.get("conformance_probe")
    if probe is None:
        return None
    require_keys(probe, {"schema_version", "action", "nonce", "mutation"})
    require_text(probe["nonce"], "conformance nonce is invalid")
    if (
        probe["schema_version"] != CONFORMANCE_PROTOCOL
        or probe["action"] != action
        or probe["mutation"] != "forbidden"
    ):
        raise BridgeError("invalid_request", "conformance probe identity is invalid")
    sentinel = f"{CONFORMANCE_SENTINEL}:{probe['nonce']}"
    reference = payload["reference"]
    if reference["external_repository"] != sentinel or reference["change_id"] != sentinel:
        raise BridgeError("invalid_request", "conformance reference is not reserved")
    if action == "merge_snapshot":
        if payload["expected_subject_revision"] != sentinel or len(payload["required_checks"]) != 1:
            raise BridgeError("invalid_request", "snapshot conformance coordinates are not reserved")
        check = payload["required_checks"][0]
        if check["key"] != sentinel or check["owner"] != sentinel:
            raise BridgeError("invalid_request", "snapshot conformance check is not reserved")
    elif payload["expected_head"] != sentinel or payload["authority_token"] != sentinel:
        raise BridgeError("invalid_request", "merge conformance coordinates are not reserved")
    return probe["nonce"]


def conformance_probe_ack(action, nonce):
    return {
        "conformance_probe": {
            "schema_version": CONFORMANCE_PROTOCOL,
            "action": action,
            "nonce": nonce,
            "mutation_performed": False,
        }
    }


def merge_snapshot(payload):
    require_keys(payload, {"reference", "expected_subject_revision", "required_checks"}, {"conformance_probe"})
    validate_reference(payload["reference"])
    require_text(payload["expected_subject_revision"], "expected subject revision is invalid")
    checks = payload["required_checks"]
    if not isinstance(checks, list):
        raise BridgeError("invalid_request", "required checks must be an array")
    seen = set()
    for check in checks:
        require_keys(check, {"provider", "key", "owner"}, {"display_name"})
        if check["provider"] != PROVIDER_KEY:
            raise BridgeError("reference_mismatch", "check provider does not match")
        require_text(check["key"], "check key is invalid")
        require_text(check["owner"], "check owner is invalid")
        identity = (check["provider"], check["key"], check["owner"])
        if identity in seen:
            raise BridgeError("invalid_request", "required check is duplicated")
        seen.add(identity)
    probe_nonce = validate_conformance_probe(payload, "merge_snapshot")

    # TODO: Fetch one coherent provider-native authority generation for exactly
    # expected_subject_revision. Return the closed author set, effective review
    # policy, current reviewer decisions, findings, conversations, exactly one
    # provider-selected conclusion per requested check, and an opaque token.
    # Report source actor IDs only. issue-spec replaces canonical principals
    # from the operator-owned registry mapping.
    # After the platform implementation and its unhappy paths are tested,
    # intercept probe_nonce locally and return conformance_probe_ack without
    # making an upstream request. The validator never accepts a normal snapshot
    # as a probe acknowledgement.
    raise BridgeError("not_implemented", "merge snapshot mapping is not implemented")


def merge_change(payload):
    require_keys(payload, {"reference", "expected_head", "authority_token"}, {"conformance_probe"})
    validate_reference(payload["reference"])
    require_text(payload["expected_head"], "expected head is invalid")
    require_text(payload["authority_token"], "authority token is invalid")
    probe_nonce = validate_conformance_probe(payload, "merge_change")

    # TODO: Use a native protected-merge primitive that atomically validates
    # expected_head and every review/check/policy fact bound by authority_token.
    # A bridge-side lock, read/read comparison, or expected-head-only merge is
    # not sufficient and must never advertise change.merge-conditional.
    # After that native implementation and its unhappy paths are tested,
    # intercept probe_nonce locally and return conformance_probe_ack. Never
    # forward the reserved reference or probe token to the provider.
    raise BridgeError("not_implemented", "conditional merge mapping is not implemented")


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
        capability_payload = {
            "protocol_version": PROTOCOL,
            "values": CAPABILITIES,
        }
        if any(value in MERGE_AUTHORITY_CAPABILITIES for value in CAPABILITIES):
            capability_payload["semantic_generation"] = SEMANTIC_GENERATION
            capability_payload["provider_build_identity"] = PROVIDER_BUILD_IDENTITY
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "capabilities": capability_payload,
        }
    if action == "merge_snapshot":
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "merge_snapshot": merge_snapshot(request.get("payload")),
        }
    if action == "merge_change":
        return {
            "protocol": PROTOCOL,
            "request_id": request["request_id"],
            "merge": merge_change(request.get("payload")),
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
    parser.add_argument("--provider-build-identity", required=True)
    parser.add_argument("--principal-mappings-file", required=True, type=Path)
    parser.add_argument("--capability", action="append", choices=sorted(OPTIONAL_CAPABILITIES), default=[])
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
    if not valid_opaque_identity(args.provider_build_identity, 256):
        raise SystemExit("--provider-build-identity must be an immutable printable identity")
    args.capability = sorted(set(args.capability) | MERGE_AUTHORITY_CAPABILITIES)
    args.principal_mapping_identity, args.principal_mappings = read_principal_mappings(
        args.principal_mappings_file
    )


def valid_opaque_identity(value, limit: int) -> bool:
    return (
        isinstance(value, str)
        and value == value.strip()
        and 0 < len(value) <= limit
        and all(0x21 <= ord(character) <= 0x7E for character in value)
    )


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def read_principal_mappings(raw_path: Path) -> tuple[str, list[dict]]:
    expanded = os.path.expanduser(str(raw_path))
    path = Path(expanded)
    if not path.is_absolute() or os.path.normpath(expanded) != expanded:
        raise SystemExit("--principal-mappings-file must be a clean absolute path")
    try:
        info = path.lstat()
    except OSError as error:
        raise SystemExit("--principal-mappings-file is unavailable") from error
    if (
        not stat.S_ISREG(info.st_mode)
        or stat.S_ISLNK(info.st_mode)
        or info.st_nlink != 1
        or info.st_size > MAXIMUM_MAPPING_BYTES
        or (os.name != "nt" and info.st_mode & 0o077 != 0)
        or (os.name != "nt" and hasattr(os, "getuid") and info.st_uid != os.getuid())
    ):
        raise SystemExit("--principal-mappings-file must be a private single-link regular file")
    descriptor = -1
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        after = os.fstat(descriptor)
        if not os.path.samestat(info, after):
            raise OSError("mapping file changed while opening")
        with os.fdopen(descriptor, "rb", closefd=True) as source:
            descriptor = -1
            raw = source.read(MAXIMUM_MAPPING_BYTES + 1)
        if len(raw) > MAXIMUM_MAPPING_BYTES:
            raise ValueError("mapping file exceeds 1 MiB")
        payload = json.loads(raw.decode("utf-8"), object_pairs_hook=strict_object)
    except (OSError, UnicodeDecodeError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit("--principal-mappings-file is not strict JSON") from error
    finally:
        if descriptor >= 0:
            os.close(descriptor)
    if not isinstance(payload, dict) or set(payload) != {"principal_mapping_identity", "principal_mappings"}:
        raise SystemExit("--principal-mappings-file shape is invalid")
    identity = payload["principal_mapping_identity"]
    mappings = payload["principal_mappings"]
    if not valid_opaque_identity(identity, 256) or not isinstance(mappings, list) or not mappings:
        raise SystemExit("principal mapping identity and at least one mapping are required")
    seen = set()
    for mapping in mappings:
        if not isinstance(mapping, dict) or set(mapping) != {"provider", "stable_id", "principal"}:
            raise SystemExit("principal mapping shape is invalid")
        principal = mapping["principal"]
        if (
            not isinstance(principal, dict)
            or set(principal) != {"realm", "stable_id"}
            or not isinstance(mapping["provider"], str)
            or PROVIDER_KEY.fullmatch(mapping["provider"]) is None
            or not valid_opaque_identity(mapping["stable_id"], 256)
            or not valid_opaque_identity(principal["realm"], 128)
            or not valid_opaque_identity(principal["stable_id"], 256)
        ):
            raise SystemExit("principal mapping identity is invalid")
        source = (mapping["provider"], mapping["stable_id"])
        if source in seen:
            raise SystemExit("principal mapping source is duplicated")
        seen.add(source)
    return identity, mappings


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
    if os.name != "nt":
        if info.st_mode & 0o077:
            raise SystemExit("output directory must use mode 0700 or stricter")
        if hasattr(os, "getuid") and info.st_uid != os.getuid():
            raise SystemExit("output directory must be owned by the current user")
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
        if os.name != "nt" and hasattr(os, "getuid") and existing.st_uid != os.getuid():
            raise SystemExit(f"refusing file not owned by current user: {path}")

    encoded = content.encode("utf-8")
    no_follow = getattr(os, "O_NOFOLLOW", 0)
    if existing is None:
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | no_follow
        try:
            descriptor = os.open(path, flags, mode)
        except OSError as error:
            raise SystemExit(f"create {path}: {error}") from error
        try:
            os.fchmod(descriptor, mode)
            with os.fdopen(descriptor, "wb", closefd=True) as output:
                output.write(encoded)
                output.flush()
                os.fsync(output.fileno())
        except Exception:
            path.unlink(missing_ok=True)
            raise
        return

    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, mode)
        with os.fdopen(descriptor, "wb", closefd=True) as output:
            output.write(encoded)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> None:
    args = parse_args()
    validate(args)
    output = prepare_output_dir(args.output)

    bridge = BRIDGE_TEMPLATE.replace("__PROVIDER_KEY__", repr(args.provider_key)).replace(
        "__PLANNED_CAPABILITIES__", repr(args.capability)
    ).replace(
        "__PROVIDER_BUILD_IDENTITY__", repr(args.provider_build_identity)
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
                "principal_mappings": args.principal_mappings,
                "principal_mapping_identity": args.principal_mapping_identity,
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

    plan = {
        "provider_key": args.provider_key,
        "semantic_generation": SEMANTIC_GENERATION,
        "provider_build_identity": args.provider_build_identity,
        "planned_capabilities": args.capability,
        "principal_mapping_identity": args.principal_mapping_identity,
        "activation": [
            "implement and contract-test merge_snapshot for one coherent exact-head native authority generation",
            "implement merge_change with native atomic expected-head and authority-token enforcement",
            "implement the reserved conformance probe acknowledgement locally in both actions with no upstream request or mutation",
            "prove every provider actor returned by merge_snapshot is covered by the operator principal mapping",
            "move implemented values into provider_bridge.py CAPABILITIES",
            "copy semantic generation and immutable provider build identity into providers.json description",
            "copy the same values into providers.json description.capabilities",
            "run validate_provider.py, confirm both runtime action probes pass, and run provider-native non-production action tests",
        ],
    }
    plan_path = output / "implementation-plan.json"
    write_file(plan_path, json.dumps(plan, indent=2) + "\n", 0o600, args.force)

    print(
        json.dumps(
            {
                "ok": True,
                "provider_key": args.provider_key,
                "capabilities": [],
                "planned_capabilities": args.capability,
                "files": [bridge_path.name, registry_path.name, plan_path.name],
                "next": "implement platform mappings, then run validate_provider.py",
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
