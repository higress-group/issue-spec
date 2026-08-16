package model

import (
	"reflect"
	"testing"
)

func typedCommentSignature(tc TypedComment) TypedComment {
	tc.Body = ""
	return tc
}

func TestTranslatedSuffixParserEquivalence(t *testing.T) {
	specBody, err := EnsureTypedBody("SPEC", "SPEC-001",
		"## Requirement: Stable export\n\nThe system MUST preserve exports.\n\n### Scenario: Export is stable\n\n- **WHEN** an export runs\n- **THEN** bytes are stable\n",
		BodyOptions{Agent: "Coordinator", Status: "confirmed", Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	taskBody, err := EnsureTypedBody("TASK", "TASK-001",
		"## Task: Keep graph stable\n\n### Execution Planning\n\n- Owner: one agent\n\n### Covers\n\n- SPEC-001\n",
		BodyOptions{Agent: "Coordinator", Status: "confirmed", Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	questionBody := testQuestionBody(t, "QUESTION-001", "Choose one.", testChoiceModel(ChoiceModeSingle, true))
	snapshot, err := SnapshotQuestion(questionBody, "https://example.test/issues/1#issuecomment-10")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := BuildAnswerPayload(snapshot, []string{"safe"}, "")
	if err != nil {
		t.Fatal(err)
	}
	answerBody := testAnswerBody(t, "ANSWER-001", payload)

	for name, original := range map[string]string{
		"SPEC":     specBody,
		"TASK":     taskBody,
		"QUESTION": questionBody,
		"ANSWER":   answerBody,
	} {
		suffixed := suffixedBody(original, original)
		t.Run(name, func(t *testing.T) {
			if got, want := typedCommentSignature(ParseTypedComment(suffixed)), typedCommentSignature(ParseTypedComment(original)); !reflect.DeepEqual(got, want) {
				t.Fatalf("ParseTypedComment differs:\ngot:  %#v\nwant: %#v", got, want)
			}
			gotMarker, gotOK, gotErr := FindMarker(suffixed)
			wantMarker, wantOK, wantErr := FindMarker(original)
			if !reflect.DeepEqual(gotMarker, wantMarker) || gotOK != wantOK || !reflect.DeepEqual(gotErr, wantErr) {
				t.Fatalf("FindMarker differs: %v/%v/%v vs %v/%v/%v", gotMarker, gotOK, gotErr, wantMarker, wantOK, wantErr)
			}
			if got, want := LogicalBody(suffixed), LogicalBody(original); got != want {
				t.Fatalf("LogicalBody differs:\ngot:  %q\nwant: %q", got, want)
			}
			if got, want := TypedSectionList(suffixed, "Covers"), TypedSectionList(original, "Covers"); !reflect.DeepEqual(got, want) {
				t.Fatalf("TypedSectionList differs: %v vs %v", got, want)
			}
			gotDiags := ValidateCanonicalBodyAtRoot(ParseTypedComment(suffixed).Type, ParseTypedComment(suffixed).ID, "", suffixed, "")
			wantDiags := ValidateCanonicalBodyAtRoot(ParseTypedComment(original).Type, ParseTypedComment(original).ID, "", original, "")
			if !reflect.DeepEqual(gotDiags, wantDiags) {
				t.Fatalf("ValidateCanonicalBodyAtRoot differs: %v vs %v", gotDiags, wantDiags)
			}
		})
	}

	t.Run("QUESTION and ANSWER payloads", func(t *testing.T) {
		questionSuffixed := suffixedBody(questionBody, questionBody)
		gotChoice, gotFound, gotErr := ParseChoiceModel(questionSuffixed)
		wantChoice, wantFound, wantErr := ParseChoiceModel(questionBody)
		if !reflect.DeepEqual(gotChoice, wantChoice) || gotFound != wantFound || !reflect.DeepEqual(gotErr, wantErr) {
			t.Fatalf("ParseChoiceModel differs: %v/%v/%v", gotChoice, gotFound, gotErr)
		}
		gotSnapshot, gotSnapshotErr := SnapshotQuestion(questionSuffixed, "https://example.test/issues/1#issuecomment-10")
		wantSnapshot, wantSnapshotErr := SnapshotQuestion(questionBody, "https://example.test/issues/1#issuecomment-10")
		if !reflect.DeepEqual(gotSnapshot, wantSnapshot) || !reflect.DeepEqual(gotSnapshotErr, wantSnapshotErr) {
			t.Fatalf("SnapshotQuestion differs: %v vs %v (%v / %v)", gotSnapshot, wantSnapshot, gotSnapshotErr, wantSnapshotErr)
		}
		answerSuffixed := suffixedBody(answerBody, answerBody)
		gotPayload, gotPayloadErr := ParseAnswerPayload(answerSuffixed)
		wantPayload, wantPayloadErr := ParseAnswerPayload(answerBody)
		if !reflect.DeepEqual(gotPayload, wantPayload) || !reflect.DeepEqual(gotPayloadErr, wantPayloadErr) {
			t.Fatalf("ParseAnswerPayload differs: %v vs %v (%v / %v)", gotPayload, wantPayload, gotPayloadErr, wantPayloadErr)
		}
	})
}

func TestUntranslatedPreviewBodyIsByteIdenticalPassthrough(t *testing.T) {
	body := "Agent: Coordinator\nType: TASK\nID: TASK-001\nStatus: confirmed\nScope: test\n\n## Task: Preview passthrough\n\n```html-preview id=demo\n<p>### Covers</p>\n\n- SPEC-999\n\n```\n\n### Covers\n\n- SPEC-001\n"
	if got := CanonicalView(body); got != body {
		t.Fatalf("CanonicalView must be byte-identical without a divider:\ngot:  %q\nwant: %q", got, body)
	}
	// TypedSectionList does not mask preview sources today; a passthrough
	// canonical view keeps that behavior unchanged.
	if got, want := TypedSectionList(body, "Covers"), []string{"SPEC-001"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TypedSectionList = %v, want %v", got, want)
	}
	want := LogicalBody(body)
	if got := LogicalBody(CanonicalView(body)); got != want {
		t.Fatalf("LogicalBody changed under passthrough: %q vs %q", got, want)
	}
}
