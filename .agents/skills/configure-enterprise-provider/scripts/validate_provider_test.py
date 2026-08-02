import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
VALIDATOR = SCRIPTS / "validate_provider.py"
REQUIRED_CAPABILITIES = [
    "evidence.review-decision",
    "evidence.authoritative-check-conclusion",
    "change.merge-conditional",
]
GENERATION = "minimal-merge-authority/v1"
BUILD = "code-example@sha256:0123456789abcdef"


class ValidateProviderTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def write_bridge(self, capability_payload):
        bridge = self.root / "bridge.py"
        bridge.write_text(
            "#!/usr/bin/env python3\n"
            "import json, sys\n"
            f"CAPABILITIES = {capability_payload!r}\n"
            "request = json.load(sys.stdin)\n"
            "json.dump({'protocol': request['protocol'], 'request_id': request['request_id'], "
            "'capabilities': CAPABILITIES}, sys.stdout)\n",
            encoding="utf-8",
        )
        bridge.chmod(0o700)
        return bridge

    def write_registry(self, described_capabilities, runtime_capabilities, *, generation="", build="", mappings=True):
        payload = {"protocol_version": "issue-spec.code-provider/v1", "values": runtime_capabilities}
        if generation:
            payload["semantic_generation"] = generation
        if build:
            payload["provider_build_identity"] = build
        bridge = self.write_bridge(payload)
        description = {
            "provider_key": "code.example",
            "display_name": "Example Code",
            "remote_authorities": ["git.example.test"],
            "code_change_label": "Merge request",
            "capabilities": described_capabilities,
            "recommended_evidence": [],
        }
        if generation:
            description["semantic_generation"] = generation
        if build:
            description["provider_build_identity"] = build
        entry = {"path": str(bridge), "description": description}
        if mappings:
            entry["principal_mapping_identity"] = "directory@sha256:0123456789abcdef"
            entry["principal_mappings"] = [
                {
                    "provider": "code.example",
                    "stable_id": "user-42",
                    "principal": {"realm": "employees", "stable_id": "person-42"},
                }
            ]
        registry = self.root / "providers.json"
        registry.write_text(
            json.dumps({"version": 1, "providers": {"code.example": entry}}), encoding="utf-8"
        )
        registry.chmod(0o600)
        return registry

    def validate(self, registry):
        return subprocess.run(
            [sys.executable, str(VALIDATOR), "--registry", str(registry), "--provider-key", "code.example"],
            text=True,
            capture_output=True,
            check=False,
        )

    def test_complete_merge_authority_provider_passes(self):
        registry = self.write_registry(REQUIRED_CAPABILITIES, REQUIRED_CAPABILITIES, generation=GENERATION, build=BUILD)
        result = self.validate(registry)
        self.assertEqual(result.returncode, 0, result.stderr)
        validated = json.loads(result.stdout)
        self.assertTrue(validated["merge_capable"])
        self.assertEqual(validated["actions"], ["merge_snapshot", "merge_change"])
        self.assertEqual(validated["principal_mapping_identity"], "directory@sha256:0123456789abcdef")

    def test_legacy_provider_fails_as_audit_only(self):
        registry = self.write_registry(["evidence.snapshot"], ["evidence.snapshot"])
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("legacy provider capabilities are audit-only", result.stderr)

    def test_partial_merge_authority_provider_reports_missing_capabilities(self):
        partial = ["evidence.review-decision"]
        registry = self.write_registry(partial, partial, generation=GENERATION, build=BUILD)
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("merge-authority capabilities are incomplete", result.stderr)
        self.assertIn("change.merge-conditional", result.stderr)

    def test_complete_provider_requires_operator_principal_mapping(self):
        registry = self.write_registry(
            REQUIRED_CAPABILITIES, REQUIRED_CAPABILITIES, generation=GENERATION, build=BUILD, mappings=False
        )
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires operator-owned principal_mappings", result.stderr)

    def test_runtime_build_must_match_operator_description(self):
        registry = self.write_registry(REQUIRED_CAPABILITIES, REQUIRED_CAPABILITIES, generation=GENERATION, build=BUILD)
        data = json.loads(registry.read_text(encoding="utf-8"))
        data["providers"]["code.example"]["description"]["provider_build_identity"] = (
            "code-example@sha256:fedcba9876543210"
        )
        registry.write_text(json.dumps(data), encoding="utf-8")
        registry.chmod(0o600)
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match operator description", result.stderr)


if __name__ == "__main__":
    unittest.main()
