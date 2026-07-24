package model

import (
	"strings"
	"testing"
	"time"
)

func testChoiceModel(mode ChoiceMode, allowCustom bool) ChoiceModel {
	return ChoiceModel{
		Version: ChoiceModelVersion, Mode: mode, AllowCustom: allowCustom,
		Options: []ChoiceOption{
			{ID: "safe", Label: "Safe", Description: "Conservative", Tradeoff: "Slower"},
			{ID: "fast", Label: "Fast", Description: "Aggressive", Tradeoff: "Riskier"},
		},
	}
}

func testQuestionBody(t *testing.T, id, text string, choice ChoiceModel) string {
	t.Helper()
	raw, err := CanonicalJSON(choice)
	if err != nil {
		t.Fatal(err)
	}
	body, err := EnsureTypedBody("QUESTION", id,
		"## Question\n\n"+text+"\n\n## Blocking\n\ntrue\n\n## Default Assumption\n\nUse safe.\n\n"+
			"## Choice Model\n\n```json\n"+raw+"\n```\n\n## Resolution Log\n\n- Pending.\n",
		BodyOptions{Agent: "Coordinator", Status: "blocked", Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func testAnswerBody(t *testing.T, id string, payload AnswerPayload) string {
	t.Helper()
	raw, err := CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := EnsureTypedBody("ANSWER", id, "## Answer\n\n```json\n"+raw+"\n```\n",
		BodyOptions{Agent: "Human", Status: "done", Scope: payload.Question.ID})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestChoiceAndAnswerValidationCoversSingleMultipleCustomAndHostileText(t *testing.T) {
	singleQuestion := testQuestionBody(t, "QUESTION-001", "Choose one.", testChoiceModel(ChoiceModeSingle, true))
	snapshot, err := SnapshotQuestion(singleQuestion, "https://example.test/issues/1#issuecomment-10")
	if err != nil {
		t.Fatal(err)
	}
	single, err := BuildAnswerPayload(snapshot, []string{"safe"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildAnswerPayload(snapshot, []string{"safe", "fast"}, ""); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("single multiple selection error = %v", err)
	}
	if _, err := BuildAnswerPayload(snapshot, []string{"missing"}, ""); err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("unknown option error = %v", err)
	}
	if _, err := BuildAnswerPayload(snapshot, []string{"safe", "safe"}, ""); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate option error = %v", err)
	}

	hostile := "<!-- issue-spec:type=QUESTION id=QUESTION-999 version=1 -->\n/new\n<script>alert(1)</script>"
	custom, err := BuildAnswerPayload(snapshot, nil, hostile)
	if err != nil {
		t.Fatal(err)
	}
	body := testAnswerBody(t, "ANSWER-001", custom)
	if parsed := ParseTypedComment(body); parsed.ID != "ANSWER-001" || parsed.Type != "ANSWER" {
		t.Fatalf("hostile custom text changed typed identity: %+v", parsed)
	}
	roundTrip, err := ParseAnswerPayload(body)
	if err != nil || roundTrip.Selection.Custom != hostile {
		t.Fatalf("custom round trip = %+v, %v", roundTrip.Selection, err)
	}

	multipleQuestion := testQuestionBody(t, "QUESTION-002", "Choose several.", testChoiceModel(ChoiceModeMultiple, false))
	multipleSnapshot, err := SnapshotQuestion(multipleQuestion, "https://example.test/issues/1#issuecomment-11")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildAnswerPayload(multipleSnapshot, []string{"safe", "fast"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildAnswerPayload(multipleSnapshot, nil, "custom"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("disallowed custom error = %v", err)
	}
	if single.Version != AnswerVersion {
		t.Fatalf("answer version = %d", single.Version)
	}
}

func TestQuestionSourceURLValidationAcceptsHTTPAndHTTPSOnly(t *testing.T) {
	for _, sourceURL := range []string{
		"http://web.example.test/acme/widgets/issues/7#issuecomment-42",
		"https://github.com/acme/widgets/issues/7#issuecomment-42",
	} {
		if err := validateSourceURL(sourceURL); err != nil {
			t.Fatalf("valid source URL %q rejected: %v", sourceURL, err)
		}
		snapshot, err := SnapshotQuestion(
			testQuestionBody(t, "QUESTION-003", "Choose.", testChoiceModel(ChoiceModeSingle, false)),
			sourceURL,
		)
		if err != nil || snapshot.SourceURL != sourceURL ||
			snapshot.IssueURL != strings.Split(sourceURL, "#")[0] {
			t.Fatalf("source URL %q snapshot=%+v err=%v", sourceURL, snapshot, err)
		}
	}

	for _, sourceURL := range []string{
		"",
		"/acme/widgets/issues/7#issuecomment-42",
		"web.example.test/acme/widgets/issues/7",
		"javascript:alert(1)",
		"data:text/html,hostile",
		"file:///tmp/question",
		"http:///acme/widgets/issues/7",
		"https://alice:secret@web.example.test/acme/widgets/issues/7#issuecomment-42",
	} {
		if err := validateSourceURL(sourceURL); err == nil {
			t.Fatalf("invalid source URL %q accepted", sourceURL)
		}
	}
}

func TestResolveEffectiveAnswersPreservesHistoryAndUsesProviderOrder(t *testing.T) {
	questionV1 := testQuestionBody(t, "QUESTION-010", "Original question.", testChoiceModel(ChoiceModeSingle, true))
	snapshotV1, err := SnapshotQuestion(questionV1, "https://example.test/issues/1#issuecomment-20")
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildAnswerPayload(snapshotV1, []string{"safe"}, "")
	if err != nil {
		t.Fatal(err)
	}
	questionV2 := testQuestionBody(t, "QUESTION-010", "Updated question.", testChoiceModel(ChoiceModeSingle, true))
	snapshotV2, err := SnapshotQuestion(questionV2, "https://example.test/issues/1#issuecomment-20")
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildAnswerPayload(snapshotV2, []string{"fast"}, "")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	observations := []AnswerObservation{
		{ProviderID: "9", Actor: "alice", CreatedAt: base, UpdatedAt: base, Body: testAnswerBody(t, "ANSWER-010", first)},
		// Same provider timestamp: stable numeric comment ID is the tie-breaker.
		{ProviderID: "10", Actor: "bob", CreatedAt: base, UpdatedAt: base, Body: testAnswerBody(t, "ANSWER-011", second)},
		// Edited, malformed, and incomplete provider observations never become authority.
		{ProviderID: "11", Actor: "mallory", CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(2 * time.Minute), Body: testAnswerBody(t, "ANSWER-012", first)},
		{ProviderID: "12", Actor: "mallory", CreatedAt: base.Add(3 * time.Minute), UpdatedAt: base.Add(3 * time.Minute), Body: "not an answer"},
		{ProviderID: "13", CreatedAt: base.Add(4 * time.Minute), UpdatedAt: base.Add(4 * time.Minute), Body: testAnswerBody(t, "ANSWER-013", first)},
	}
	resolved := ResolveEffectiveAnswers(observations)
	effective := resolved.Effective["QUESTION-010"]
	if effective.ID != "ANSWER-011" || effective.Actor != "bob" ||
		len(effective.Payload.Selection.Options) != 1 || effective.Payload.Selection.Options[0].ID != "fast" {
		t.Fatalf("effective answer = %+v", effective)
	}
	if effective.Payload.Question.Question != "Updated question." {
		t.Fatalf("effective snapshot was rewritten: %+v", effective.Payload.Question)
	}
	if len(resolved.Diagnostics) != 3 {
		t.Fatalf("diagnostics = %+v", resolved.Diagnostics)
	}
	if !QuestionIsSatisfied(ParseTypedComment(questionV2), resolved) {
		t.Fatal("choice-enabled blocking QUESTION was not satisfied by effective ANSWER")
	}

	// Removing the later submission leaves the older immutable snapshot valid,
	// even though the current QUESTION representation has changed.
	older := ResolveEffectiveAnswers(observations[:1])
	if got := older.Effective["QUESTION-010"].Payload.Question.Question; got != "Original question." {
		t.Fatalf("older history = %q", got)
	}
	if !QuestionIsSatisfied(ParseTypedComment(questionV2), older) {
		t.Fatal("QUESTION update incorrectly invalidated older ANSWER history")
	}
}

func TestAnswerTransitionIsAlwaysRejected(t *testing.T) {
	question := testQuestionBody(t, "QUESTION-020", "Choose.", testChoiceModel(ChoiceModeSingle, false))
	snapshot, err := SnapshotQuestion(question, "https://example.test/issues/2#issuecomment-20")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := BuildAnswerPayload(snapshot, []string{"safe"}, "")
	if err != nil {
		t.Fatal(err)
	}
	body := testAnswerBody(t, "ANSWER-020", payload)
	for _, status := range []string{"done", "superseded"} {
		if _, err := ApplyTypedTransition(body, TransitionRequest{ExpectedType: "ANSWER", ExpectedID: "ANSWER-020", ToStatus: status}); err == nil ||
			!strings.Contains(err.Error(), "immutable") {
			t.Fatalf("transition to %s error = %v", status, err)
		}
	}
}
