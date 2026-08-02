package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
)

func TestRoleCommandHelpAndReadOnlyReceiptDiagnostics(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute([]string{"role", "complete", "--help"}, strings.NewReader(""), &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "issue-spec role complete") || !strings.Contains(out.String(), "--assignment-file") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	receipt, err := assignment.SealReceipt(assignment.Receipt{
		SchemaVersion: assignment.ReceiptSchemaVersion, ID: "receipt:diagnostic", AssignmentID: "assignment:diagnostic",
		AssignmentDigest: strings.Repeat("a", 64), AssignmentGeneration: 1, Role: assignment.RoleImplementation,
		ResultSchemaVersion: assignment.ReceiptSchemaVersion, BaseRevision: strings.Repeat("b", 40), ResultRevision: strings.Repeat("c", 40),
		Provenance: assignment.Provenance{Route: assignment.RouteRoleOwned, Assurance: assignment.AssuranceSelfReported,
			Writer: "Worker", Subject: "Worker", Source: "role-complete"},
		Implementation: &assignment.ImplementationResult{ChangedPaths: []string{"marker.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\r', '\n', ' ', '\t')
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := Execute([]string{"role", "verify-receipt", "--receipt-file", path, "--json"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("verify code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var report assignment.ReceiptInspection
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Valid || report.RecomputedDigest != receipt.ReceiptDigest {
		t.Fatalf("report=%+v err=%v output=%q", report, err, out.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatalf("diagnostics mutated input err=%v", err)
	}
}

func TestRoleVerifyReceiptReportsMismatchWithoutRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	data := []byte(`{"schema_version":"issue-spec.receipt/v1","receipt_id":"bad","receipt_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Execute([]string{"role", "verify-receipt", "--receipt-file", path, "--json"}, strings.NewReader(""), &out, &errOut); code == 0 {
		t.Fatalf("invalid receipt succeeded: %q", out.String())
	}
	before, _ := os.ReadFile(path)
	if !bytes.Equal(before, data) {
		t.Fatal("invalid receipt was repaired")
	}
}
