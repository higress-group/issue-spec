import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parent
VALIDATOR = SCRIPTS / "validate_provider.py"
CAPABILITIES = ["change.comment", "change.create", "evidence.snapshot"]


class ValidateProviderTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve(strict=True)

    def write_bridge(self, runtime_capabilities, mode="normal"):
        bridge = self.root / "bridge.py"
        bridge.write_text(
            "#!/usr/bin/env python3\n"
            "import json, sys\n"
            f"VALUES = {runtime_capabilities!r}\n"
            f"MODE = {mode!r}\n"
            "request = json.load(sys.stdin)\n"
            "if MODE == 'malformed':\n"
            "    sys.stdout.write('{')\n"
            "    raise SystemExit(0)\n"
            "if MODE == 'overflow':\n"
            "    sys.stdout.write('x' * (2 * 1024 * 1024))\n"
            "    raise SystemExit(0)\n"
            "response = {\n"
            "  'protocol': request['protocol'],\n"
            "  'request_id': request['request_id'],\n"
            "  'capabilities': {'protocol_version': request['protocol'], 'values': VALUES},\n"
            "}\n"
            "if MODE == 'wrong_identity': response['request_id'] += '-wrong'\n"
            "json.dump(response, sys.stdout)\n",
            encoding="utf-8",
        )
        bridge.chmod(0o700)
        return bridge

    def write_registry(self, described, runtime=None, *, description_extra=None, mode="normal"):
        if runtime is None:
            runtime = described
        bridge = self.write_bridge(runtime, mode)
        description = {
            "display_name": "Example Code",
            "remote_authorities": ["git.example.test"],
            "code_change_label": "Merge request",
            "capabilities": described,
            "recommended_evidence": [],
        }
        description.update(description_extra or {})
        registry = self.root / "providers.json"
        registry.write_text(json.dumps({
            "version": 1,
            "providers": {
                "code.example": {
                    "path": str(bridge),
                    "environment": ["CODE_EXAMPLE_TOKEN_FILE=/run/secrets/token"],
                    "timeout": "30s",
                    "max_output_bytes": 1048576,
                    "description": description,
                }
            },
        }), encoding="utf-8")
        registry.chmod(0o600)
        return registry

    def validate(self, registry):
        return subprocess.run(
            [sys.executable, str(VALIDATOR), "--registry", str(registry),
             "--provider-key", "code.example"],
            text=True, capture_output=True, check=False,
        )

    def test_operation_capabilities_pass_without_merge_authority(self):
        result = self.validate(self.write_registry(CAPABILITIES))
        self.assertEqual(result.returncode, 0, result.stderr)
        payload = json.loads(result.stdout)
        self.assertEqual(payload["capabilities"], CAPABILITIES)
        self.assertEqual(payload["operations"], {
            "create_change": True, "comment": True, "snapshot": True,
        })

    def test_inert_provider_is_valid_configuration(self):
        result = self.validate(self.write_registry([]))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["capabilities"], [])

    def test_runtime_and_description_must_match(self):
        result = self.validate(self.write_registry(["change.create"], []))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("do not match operator description", result.stderr)

    def test_unknown_or_duplicate_capabilities_are_rejected(self):
        for capabilities in (["change.merge-conditional"], ["change.create", "change.create"]):
            with self.subTest(capabilities=capabilities):
                result = self.validate(self.write_registry(capabilities))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("capabilities are invalid", result.stderr)

    def test_retired_authority_metadata_is_rejected(self):
        result = self.validate(self.write_registry([], description_extra={
            "semantic_generation": "minimal-merge-authority/v1",
        }))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unsupported fields", result.stderr)

    def test_malformed_or_identity_mismatched_handshake_is_rejected(self):
        for mode, diagnostic in (("malformed", "strict JSON"), ("wrong_identity", "identity")):
            with self.subTest(mode=mode):
                result = self.validate(self.write_registry([], mode=mode))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(diagnostic, result.stderr)

    def test_registry_must_be_private(self):
        registry = self.write_registry([])
        registry.chmod(0o644)
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("0600", result.stderr)

    def test_output_is_bounded_while_the_provider_runs(self):
        result = self.validate(self.write_registry([], mode="overflow"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("output exceeded", result.stderr)

    def test_runtime_compatible_compound_duration_is_accepted(self):
        registry = self.write_registry([])
        payload = json.loads(registry.read_text(encoding="utf-8"))
        payload["providers"]["code.example"]["timeout"] = "1m30s"
        registry.write_text(json.dumps(payload), encoding="utf-8")
        result = self.validate(registry)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_every_registry_entry_is_validated(self):
        registry = self.write_registry([])
        payload = json.loads(registry.read_text(encoding="utf-8"))
        payload["providers"]["unselected"] = {"path": "relative/provider"}
        registry.write_text(json.dumps(payload), encoding="utf-8")
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("path must be clean and absolute", result.stderr)


if __name__ == "__main__":
    unittest.main()
