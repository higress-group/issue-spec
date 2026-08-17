package model

import (
	"reflect"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/assignment"
)

func TestSpecializedMarkerParsersTolerateTranslatedBodies(t *testing.T) {
	rationaleBody := RenderRationaleMarker("PROCESS-001", "SPEC-001", "internal/model/translation.go", 28) +
		"\n\n### Rationale\n\nThe suffix must not hide the marker.\n"
	findingBody := RenderFindingMarker("FINDING-001", "P2", "PROCESS-001", "SPEC-001", "open", "internal/x.go", 12) +
		"\n\nFinding text.\n"
	findingReplyBody := RenderFindingReplyMarker("FINDING-001", "PROCESS-001", "resolved") + "\n\nReply text.\n"
	rationaleCCBody, err := RenderCodeChangeRationaleBody(testCodeChangeRationaleMarker(),
		"This implementation keeps the provider boundary explicit.")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rationale marker", func(t *testing.T) {
		want, wantFound, wantErr := FindRationaleMarker(rationaleBody)
		got, gotFound, gotErr := FindRationaleMarker(suffixedBody(rationaleBody, rationaleBody))
		if gotFound != wantFound || !reflect.DeepEqual(gotErr, wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v/%v/%v want=%+v/%v/%v", got, gotFound, gotErr, want, wantFound, wantErr)
		}
	})

	t.Run("finding marker", func(t *testing.T) {
		want, wantFound, wantErr := FindFindingMarker(findingBody)
		got, gotFound, gotErr := FindFindingMarker(suffixedBody(findingBody, findingBody))
		if gotFound != wantFound || !reflect.DeepEqual(gotErr, wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v/%v/%v want=%+v/%v/%v", got, gotFound, gotErr, want, wantFound, wantErr)
		}
	})

	t.Run("finding reply marker", func(t *testing.T) {
		want, wantFound, wantErr := FindFindingReplyMarker(findingReplyBody)
		got, gotFound, gotErr := FindFindingReplyMarker(suffixedBody(findingReplyBody, findingReplyBody))
		if gotFound != wantFound || !reflect.DeepEqual(gotErr, wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v/%v/%v want=%+v/%v/%v", got, gotFound, gotErr, want, wantFound, wantErr)
		}
	})

	t.Run("code-change rationale marker", func(t *testing.T) {
		want, wantFound, wantErr := FindCodeChangeRationaleMarker(rationaleCCBody)
		if !wantFound || wantErr != nil {
			t.Fatalf("fixture must parse: found=%v err=%v", wantFound, wantErr)
		}
		got, gotFound, gotErr := FindCodeChangeRationaleMarker(suffixedBody(rationaleCCBody, rationaleCCBody))
		if gotFound != wantFound || gotErr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("translated got=%+v/%v/%v want=%+v/%v/%v", got, gotFound, gotErr, want, wantFound, wantErr)
		}
		genuine := rationaleCCBody + rationaleCCBody
		if _, found, err := FindCodeChangeRationaleMarker(genuine); !found || err == nil ||
			!strings.Contains(err.Error(), "exactly one marker") {
			t.Fatalf("genuine duplicate found=%v err=%v", found, err)
		}
	})
}

func TestObserveAcceptedReceiptAuthorityToleratesTranslatedCarrier(t *testing.T) {
	payload := `{"receipt_id":"receipt-1","receipt_digest":"` + strings.Repeat("a", 64) +
		`","assignment_generation":2}`
	start := "<!-- issue-spec:accepted-implementation-receipt version=1 -->"
	end := "<!-- /issue-spec:accepted-implementation-receipt -->"
	body := "typed carrier\n\n" + start + "\n" + payload + "\n" + end + "\n"

	want, wantFound, wantErr := ObserveAcceptedReceiptAuthority(body, assignment.RoleImplementation)
	if !wantFound || wantErr != nil {
		t.Fatalf("fixture must parse: found=%v err=%v", wantFound, wantErr)
	}
	translated := suffixedBody(body, body)
	got, gotFound, gotErr := ObserveAcceptedReceiptAuthority(translated, assignment.RoleImplementation)
	if gotFound != wantFound || gotErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("translated got=%+v/%v/%v want=%+v/%v/%v", got, gotFound, gotErr, want, wantFound, wantErr)
	}
	genuine := body + start + "\n" + payload + "\n" + end + "\n"
	if _, found, err := ObserveAcceptedReceiptAuthority(genuine, assignment.RoleImplementation); !found || err == nil ||
		!strings.Contains(err.Error(), "exactly one recognized marker pair") {
		t.Fatalf("genuine duplicate found=%v err=%v", found, err)
	}
}
