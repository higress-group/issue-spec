import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
SCAFFOLD = SCRIPTS / "scaffold_provider.py"
VALIDATOR = SCRIPTS / "validate_provider.py"
REQUIRED_CAPABILITIES = {
    "evidence.review-decision",
    "evidence.authoritative-check-conclusion",
    "change.merge-conditional",
}


class ScaffoldProviderTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.mapping = self.root / "principal-mappings.json"
        self.mapping.write_text(
            json.dumps(
                {
                    "principal_mapping_identity": "directory@sha256:0123456789abcdef",
                    "principal_mappings": [
                        {
                            "provider": "code.example",
                            "stable_id": "user-42",
                            "principal": {"realm": "employees", "stable_id": "person-42"},
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        self.mapping.chmod(0o600)
        self.output = self.root / "provider"

    def scaffold(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCAFFOLD),
                "--provider-key",
                "code.example",
                "--display-name",
                "Example Code",
                "--remote-authority",
                "git.example.test",
                "--provider-build-identity",
                "code-example@sha256:0123456789abcdef",
                "--principal-mappings-file",
                str(self.mapping),
                "--output",
                str(self.output),
            ],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def invoke(self, request):
        return json.loads(
            subprocess.run(
                [str(self.output / "provider_bridge.py")],
                input=json.dumps(request),
                text=True,
                capture_output=True,
                check=True,
            ).stdout
        )

    def test_scaffold_plans_complete_merge_authority_but_stays_inert(self):
        result = self.scaffold()
        self.assertEqual(set(result["planned_capabilities"]), REQUIRED_CAPABILITIES)
        self.assertEqual(result["capabilities"], [])

        registry = json.loads((self.output / "providers.json").read_text(encoding="utf-8"))
        entry = registry["providers"]["code.example"]
        self.assertEqual(entry["description"]["capabilities"], [])
        self.assertEqual(entry["principal_mapping_identity"], "directory@sha256:0123456789abcdef")
        self.assertEqual(entry["principal_mappings"][0]["stable_id"], "user-42")

        response = self.invoke(
            {
                "protocol": "issue-spec.code-provider/v1",
                "request_id": "capabilities-1",
                "action": "capabilities",
                "payload": None,
            }
        )
        self.assertEqual(response["capabilities"], {"protocol_version": "issue-spec.code-provider/v1", "values": []})

    def test_scaffold_understands_exact_head_snapshot_and_token_bound_merge(self):
        self.scaffold()
        reference = {
            "provider_key": "code.example",
            "external_repository": "acme/widgets",
            "change_id": "42",
        }
        snapshot = self.invoke(
            {
                "protocol": "issue-spec.code-provider/v1",
                "request_id": "snapshot-1",
                "action": "merge_snapshot",
                "payload": {
                    "reference": reference,
                    "expected_subject_revision": "abc123",
                    "required_checks": [
                        {"provider": "code.example", "key": "app:7/context:unit", "owner": "app:7"}
                    ],
                },
            }
        )
        self.assertEqual(snapshot["error"]["code"], "not_implemented")

        missing_token = self.invoke(
            {
                "protocol": "issue-spec.code-provider/v1",
                "request_id": "merge-1",
                "action": "merge_change",
                "payload": {"reference": reference, "expected_head": "abc123"},
            }
        )
        self.assertEqual(missing_token["error"]["code"], "invalid_request")
        merge = self.invoke(
            {
                "protocol": "issue-spec.code-provider/v1",
                "request_id": "merge-2",
                "action": "merge_change",
                "payload": {
                    "reference": reference,
                    "expected_head": "abc123",
                    "authority_token": "provider-generation-9",
                },
            }
        )
        self.assertEqual(merge["error"]["code"], "not_implemented")

    def test_completed_scaffold_registration_passes_public_validator(self):
        self.scaffold()
        bridge_path = self.output / "provider_bridge.py"
        bridge = bridge_path.read_text(encoding="utf-8")
        bridge = bridge.replace("CAPABILITIES = []", "CAPABILITIES = PLANNED_CAPABILITIES", 1)
        bridge_path.write_text(bridge, encoding="utf-8")
        bridge_path.chmod(0o750)

        registry_path = self.output / "providers.json"
        registry = json.loads(registry_path.read_text(encoding="utf-8"))
        description = registry["providers"]["code.example"]["description"]
        description["semantic_generation"] = "minimal-merge-authority/v1"
        description["provider_build_identity"] = "code-example@sha256:0123456789abcdef"
        description["capabilities"] = sorted(REQUIRED_CAPABILITIES)
        registry_path.write_text(json.dumps(registry), encoding="utf-8")
        registry_path.chmod(0o600)

        result = subprocess.run(
            [
                sys.executable,
                str(VALIDATOR),
                "--registry",
                str(registry_path),
                "--provider-key",
                "code.example",
            ],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)["merge_capable"])


if __name__ == "__main__":
    unittest.main()
