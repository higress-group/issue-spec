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
        self.mutation_log = self.root / "mutation.log"

    def write_bridge(self, capability_payload, behavior=None):
        if behavior is None:
            behavior = {"merge_snapshot": "ack", "merge_change": "ack"}
        bridge = self.root / "bridge.py"
        bridge.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            "from pathlib import Path\n"
            f"CAPABILITIES = {capability_payload!r}\n"
            f"BEHAVIOR = {behavior!r}\n"
            "request = json.load(sys.stdin)\n"
            "action = request['action']\n"
            "if action == 'capabilities':\n"
            "    json.dump({'protocol': request['protocol'], 'request_id': request['request_id'], "
            "'capabilities': CAPABILITIES}, sys.stdout)\n"
            "    raise SystemExit(0)\n"
            "payload = request.get('payload') or {}\n"
            "probe = payload.get('conformance_probe')\n"
            "nonce = probe.get('nonce', '') if isinstance(probe, dict) else ''\n"
            "sentinel = '__issue_spec_conformance_probe__:' + nonce\n"
            "reference = payload.get('reference') or {}\n"
            "reserved = (\n"
            "    isinstance(probe, dict)\n"
            "    and probe == {\n"
            "        'schema_version': 'issue-spec.code-provider-conformance/v1',\n"
            "        'action': action, 'nonce': nonce, 'mutation': 'forbidden'\n"
            "    }\n"
            "    and reference.get('provider_key') == 'code.example'\n"
            "    and reference.get('external_repository') == sentinel\n"
            "    and reference.get('change_id') == sentinel\n"
            ")\n"
            "if action == 'merge_snapshot':\n"
            "    checks = payload.get('required_checks') or []\n"
            "    reserved = (\n"
            "        reserved and payload.get('expected_subject_revision') == sentinel\n"
            "        and len(checks) == 1 and checks[0].get('provider') == 'code.example'\n"
            "        and checks[0].get('key') == sentinel\n"
            "        and checks[0].get('owner') == sentinel\n"
            "    )\n"
            "else:\n"
            "    reserved = (\n"
            "        reserved and payload.get('expected_head') == sentinel\n"
            "        and payload.get('authority_token') == sentinel\n"
            "    )\n"
            "if not reserved:\n"
            "    Path(os.environ['CONFORMANCE_MUTATION_LOG']).write_text('unsafe provider request', encoding='utf-8')\n"
            "mode = BEHAVIOR.get(action, 'unsupported_action')\n"
            "if mode == 'malformed':\n"
            "    sys.stdout.write('{\"protocol\":')\n"
            "    raise SystemExit(0)\n"
            "response = {'protocol': request['protocol'], 'request_id': request['request_id']}\n"
            "if mode == 'request_identity':\n"
            "    response['request_id'] += '-wrong'\n"
            "if mode == 'protocol_identity':\n"
            "    response['protocol'] = 'issue-spec.code-provider/v0'\n"
            "if mode in ('not_implemented', 'unsupported_action'):\n"
            "    response['error'] = {'code': mode, 'message': 'fixture rejection'}\n"
            "else:\n"
            "    field = 'merge_snapshot' if action == 'merge_snapshot' else 'merge'\n"
            "    if mode == 'success':\n"
            "        response[field] = {'reference': payload.get('reference'), 'merged': action == 'merge_change'}\n"
            "    else:\n"
            "        acknowledgement = {\n"
            "            'schema_version': 'issue-spec.code-provider-conformance/v1',\n"
            "            'action': action,\n"
            "            'nonce': probe.get('nonce') if isinstance(probe, dict) else '',\n"
            "            'mutation_performed': mode == 'mutation_true',\n"
            "        }\n"
            "        if mode == 'action_identity':\n"
            "            acknowledgement['action'] = 'merge_change' if action == 'merge_snapshot' else 'merge_snapshot'\n"
            "        if mode == 'nonce_identity':\n"
            "            acknowledgement['nonce'] += '-wrong'\n"
            "        response[field] = {'conformance_probe': acknowledgement}\n"
            "json.dump(response, sys.stdout)\n",
            encoding="utf-8",
        )
        bridge.chmod(0o700)
        return bridge

    def write_registry(
        self,
        described_capabilities,
        runtime_capabilities,
        *,
        generation="",
        build="",
        mappings=True,
        behavior=None,
    ):
        payload = {"protocol_version": "issue-spec.code-provider/v1", "values": runtime_capabilities}
        if generation:
            payload["semantic_generation"] = generation
        if build:
            payload["provider_build_identity"] = build
        bridge = self.write_bridge(payload, behavior)
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
        entry = {
            "path": str(bridge),
            "environment": [f"CONFORMANCE_MUTATION_LOG={self.mutation_log}"],
            "description": description,
        }
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

    def complete_registry(self, behavior=None):
        return self.write_registry(
            REQUIRED_CAPABILITIES,
            REQUIRED_CAPABILITIES,
            generation=GENERATION,
            build=BUILD,
            behavior=behavior,
        )

    def assert_zero_mutation(self):
        self.assertFalse(self.mutation_log.exists(), "validator allowed a probe to reach the mutation path")

    def test_complete_probe_aware_merge_authority_provider_passes(self):
        result = self.validate(self.complete_registry())
        self.assertEqual(result.returncode, 0, result.stderr)
        validated = json.loads(result.stdout)
        self.assertTrue(validated["merge_capable"])
        self.assertEqual(validated["actions"], ["merge_snapshot", "merge_change"])
        self.assertEqual(validated["principal_mapping_identity"], "directory@sha256:0123456789abcdef")
        self.assertEqual(validated["conformance"]["schema_version"], "issue-spec.code-provider-conformance/v1")
        self.assertFalse(validated["conformance"]["mutation_performed"])
        self.assert_zero_mutation()

    def test_legacy_provider_fails_as_audit_only(self):
        registry = self.write_registry(["evidence.snapshot"], ["evidence.snapshot"])
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("legacy provider capabilities are audit-only", result.stderr)
        self.assert_zero_mutation()

    def test_partial_merge_authority_provider_reports_missing_capabilities(self):
        partial = ["evidence.review-decision"]
        registry = self.write_registry(partial, partial, generation=GENERATION, build=BUILD)
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("merge-authority capabilities are incomplete", result.stderr)
        self.assertIn("change.merge-conditional", result.stderr)
        self.assert_zero_mutation()

    def test_complete_provider_requires_operator_principal_mapping(self):
        registry = self.write_registry(
            REQUIRED_CAPABILITIES, REQUIRED_CAPABILITIES, generation=GENERATION, build=BUILD, mappings=False
        )
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires operator-owned principal_mappings", result.stderr)
        self.assert_zero_mutation()

    def test_runtime_build_must_match_operator_description(self):
        registry = self.complete_registry()
        data = json.loads(registry.read_text(encoding="utf-8"))
        data["providers"]["code.example"]["description"]["provider_build_identity"] = (
            "code-example@sha256:fedcba9876543210"
        )
        registry.write_text(json.dumps(data), encoding="utf-8")
        registry.chmod(0o600)
        result = self.validate(registry)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match operator description", result.stderr)
        self.assert_zero_mutation()

    def test_not_implemented_and_unsupported_actions_both_fail_closed(self):
        for mode in ("not_implemented", "unsupported_action"):
            with self.subTest(mode=mode):
                self.mutation_log.unlink(missing_ok=True)
                behavior = {"merge_snapshot": mode, "merge_change": mode}
                result = self.validate(self.complete_registry(behavior))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(f"merge_snapshot returned {mode}", result.stderr)
                self.assertIn(f"merge_change returned {mode}", result.stderr)
                self.assert_zero_mutation()

    def test_malformed_and_identity_mismatched_probe_responses_fail_closed(self):
        cases = [
            ({"merge_snapshot": "malformed", "merge_change": "ack"}, "not one strict JSON object"),
            ({"merge_snapshot": "action_identity", "merge_change": "request_identity"}, "identity"),
            ({"merge_snapshot": "nonce_identity", "merge_change": "mutation_true"}, "mutation result"),
        ]
        for behavior, diagnostic in cases:
            with self.subTest(behavior=behavior):
                self.mutation_log.unlink(missing_ok=True)
                result = self.validate(self.complete_registry(behavior))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(diagnostic, result.stderr)
                self.assert_zero_mutation()

    def test_normal_action_success_is_rejected_without_mutation(self):
        result = self.validate(
            self.complete_registry({"merge_snapshot": "success", "merge_change": "success"})
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("merge_snapshot returned a normal action result", result.stderr)
        self.assertIn("merge_change returned a normal action result", result.stderr)
        self.assert_zero_mutation()


if __name__ == "__main__":
    unittest.main()
