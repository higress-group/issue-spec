package assignment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestInspectReceiptJSONUsesLogicalFramingAndNeverRepairs(t *testing.T) {
	receipt, err := SealReceipt(Receipt{
		SchemaVersion: ReceiptSchemaVersion, ID: "receipt:test", AssignmentID: "assignment:test",
		AssignmentDigest: strings.Repeat("a", 64), AssignmentGeneration: 1, Role: RoleImplementation,
		ResultSchemaVersion: ReceiptSchemaVersion, BaseRevision: strings.Repeat("b", 40), ResultRevision: strings.Repeat("c", 40),
		Provenance:     Provenance{Route: RouteRoleOwned, Assurance: AssuranceSelfReported, Writer: "Worker", Subject: "Worker", Source: "role-complete"},
		Implementation: &ImplementationResult{ChangedPaths: []string{"marker.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	framings := [][]byte{
		compact,
		append(append([]byte(nil), compact...), '\n'),
		append(append([]byte(nil), compact...), '\r', '\n'),
		append(append([]byte(nil), indented...), []byte(" \r\n\t")...),
	}
	for index, framing := range framings {
		before := append([]byte(nil), framing...)
		report := InspectReceiptJSON(framing)
		if !report.Valid || !report.StructuralValid || !report.DigestMatches || report.ProvidedDigest != receipt.ReceiptDigest || report.RecomputedDigest != receipt.ReceiptDigest {
			t.Fatalf("framing %d report=%+v", index, report)
		}
		if !bytes.Equal(before, framing) {
			t.Fatalf("framing %d was mutated", index)
		}
	}

	tampered := bytes.Replace(compact, []byte(`"marker.txt"`), []byte(`"other.txt"`), 1)
	report := InspectReceiptJSON(tampered)
	if !report.StructuralValid || report.Valid || report.DigestMatches || report.RecomputedDigest == receipt.ReceiptDigest {
		t.Fatalf("semantic tamper report=%+v", report)
	}
	if parsed, parseErr := ParseReceiptJSON(tampered); parseErr == nil || parsed.ReceiptDigest != "" {
		t.Fatalf("ParseReceiptJSON repaired tamper: parsed=%+v err=%v", parsed, parseErr)
	}

	unknown := bytes.Replace(compact, []byte(`{"schema_version"`), []byte(`{"unknown":true,"schema_version"`), 1)
	if report := InspectReceiptJSON(unknown); report.StructuralValid || report.Valid || len(report.Errors) == 0 {
		t.Fatalf("unknown-field report=%+v", report)
	}
	multiple := append(append([]byte(nil), compact...), []byte(" {}")...)
	if report := InspectReceiptJSON(multiple); report.StructuralValid || report.Valid || len(report.Errors) == 0 {
		t.Fatalf("multiple-value report=%+v", report)
	}
}
