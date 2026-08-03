import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parent
SCAFFOLD = SCRIPTS / "scaffold_provider.py"
VALIDATOR = SCRIPTS / "validate_provider.py"
CAPABILITIES = ["change.comment", "change.create", "evidence.snapshot"]


class ScaffoldProviderTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve(strict=True)
        self.output = self.root / "provider"

    def scaffold(self, extra=()):
        command = [
            sys.executable, str(SCAFFOLD),
            "--provider-key", "code.example",
            "--display-name", "Example Code",
            "--remote-authority", "git.example.test",
            "--output", str(self.output),
        ]
        for capability in CAPABILITIES:
            command += ["--capability", capability]
        command += list(extra)
        return subprocess.run(command, text=True, capture_output=True, check=False)

    def invoke(self, request):
        return json.loads(subprocess.run(
            [str(self.output / "provider_bridge.py")], input=json.dumps(request),
            text=True, capture_output=True, check=True,
        ).stdout)

    def test_scaffold_is_inert_and_plans_only_selected_operations(self):
        result = self.scaffold()
        self.assertEqual(result.returncode, 0, result.stderr)
        rendered = json.loads(result.stdout)
        self.assertEqual(rendered["planned_capabilities"], CAPABILITIES)
        self.assertEqual(rendered["capabilities"], [])

        registry = json.loads((self.output / "providers.json").read_text(encoding="utf-8"))
        entry = registry["providers"]["code.example"]
        self.assertEqual(entry["description"]["capabilities"], [])
        self.assertNotIn("principal_mappings", entry)
        self.assertNotIn("semantic_generation", entry["description"])

        response = self.invoke({
            "protocol": "issue-spec.code-provider/v1",
            "request_id": "capabilities-1",
            "action": "capabilities",
        })
        self.assertEqual(response["capabilities"], {
            "protocol_version": "issue-spec.code-provider/v1", "values": [],
        })

    def test_unimplemented_operation_handlers_return_bounded_errors(self):
        self.assertEqual(self.scaffold().returncode, 0)
        reference = {
            "provider_key": "code.example",
            "external_repository": "acme/widgets",
            "change_id": "42",
        }
        snapshot = self.invoke({
            "protocol": "issue-spec.code-provider/v1", "request_id": "snapshot-1",
            "action": "snapshot", "payload": {"reference": reference, "subject_revision": "abc123"},
        })
        self.assertEqual(snapshot["error"]["code"], "not_implemented")
        comment = self.invoke({
            "protocol": "issue-spec.code-provider/v1", "request_id": "comment-1",
            "action": "mutate", "payload": {
                "kind": "comment", "reference": reference, "body": "why", "head_revision": "abc123",
            },
        })
        self.assertEqual(comment["error"]["code"], "not_implemented")

    def test_activated_handshake_validates_without_merge_probes(self):
        self.assertEqual(self.scaffold().returncode, 0)
        bridge_path = self.output / "provider_bridge.py"
        bridge = bridge_path.read_text(encoding="utf-8").replace(
            "CAPABILITIES = []", "CAPABILITIES = PLANNED_CAPABILITIES", 1
        )
        bridge_path.write_text(bridge, encoding="utf-8")
        bridge_path.chmod(0o750)
        registry_path = self.output / "providers.json"
        registry = json.loads(registry_path.read_text(encoding="utf-8"))
        registry["providers"]["code.example"]["description"]["capabilities"] = CAPABILITIES
        registry_path.write_text(json.dumps(registry), encoding="utf-8")
        registry_path.chmod(0o600)

        result = subprocess.run(
            [sys.executable, str(VALIDATOR), "--registry", str(registry_path),
             "--provider-key", "code.example"],
            text=True, capture_output=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        validated = json.loads(result.stdout)
        self.assertEqual(validated["capabilities"], CAPABILITIES)
        self.assertTrue(validated["operations"]["create_change"])
        self.assertIn("does not replace", validated["note"])

    def test_retired_merge_authority_arguments_are_rejected(self):
        result = self.scaffold(["--provider-build-identity", "build-1"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unrecognized arguments", result.stderr)


if __name__ == "__main__":
    unittest.main()
