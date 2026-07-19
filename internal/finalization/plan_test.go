package finalization

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

const (
	testSubject      = "1111111111111111111111111111111111111111"
	testProviderBase = "4444444444444444444444444444444444444444"
	testBaseline     = "2222222222222222222222222222222222222222"
	testEvidence     = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

func TestCompileDeterministicFrozenPlanAndOrdering(t *testing.T) {
	observations := planFixture(t)
	input := CompileInput{Repository: "o/r", Hostname: "github.com", Proposal: 1, Design: 2, Implement: 3,
		Subject:      Subject{PullRequest: 9, URL: "https://github.com/o/r/pull/9", SubjectRevision: testSubject, ProviderBaseRevision: testProviderBase, BaselineRevision: testBaseline, ProviderEvidenceDigest: testEvidence},
		Intent:       Intent{Version: 1, BaselineRevision: testBaseline, SupersededBy: []IntentEdge{{From: "PROCESS-001", To: "PROCESS-002"}}},
		Observations: observations, LifecycleReady: true}
	first, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Observations = append([]Observation(nil), observations...)
	for left, right := 0, len(input.Observations)-1; left < right; left, right = left+1, right-1 {
		input.Observations[left], input.Observations[right] = input.Observations[right], input.Observations[left]
	}
	second, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanDigest != second.PlanDigest {
		t.Fatalf("plan digest is not deterministic: %s != %s", first.PlanDigest, second.PlanDigest)
	}
	wantPrefixes := []string{"01-stamp-", "02-link-", "04-supersede-", "05-complete-", "06-complete-"}
	if len(first.Reconcile.Operations) != len(wantPrefixes) {
		t.Fatalf("operations=%#v", first.Reconcile.Operations)
	}
	for i, prefix := range wantPrefixes {
		if !strings.HasPrefix(first.Reconcile.Operations[i].ID, prefix) {
			t.Fatalf("operation[%d]=%s, want prefix %s", i, first.Reconcile.Operations[i].ID, prefix)
		}
	}
	link := first.Reconcile.Operations[1]
	if len(link.Precondition.Endpoints) != 2 || link.Precondition.RepresentationVersion != 0 || link.Precondition.BodyDigest != "" {
		t.Fatalf("link endpoint contract=%+v", link.Precondition)
	}
	for _, endpoint := range link.Precondition.Endpoints {
		if endpoint.AfterDigest == "" || (endpoint.RepresentationVersion == 0 && endpoint.BodyDigest == "") {
			t.Fatalf("incomplete endpoint contract=%+v", endpoint)
		}
	}
	for _, transition := range first.Reconcile.Operations[2:4] {
		if transition.Precondition.BodyDigest == "" {
			t.Fatalf("transition %s did not bind its planned predecessor representation: %+v", transition.ID, transition.Precondition)
		}
	}
	task := first.Reconcile.Operations[len(first.Reconcile.Operations)-1]
	if len(task.DependsOn) != len(first.Reconcile.Operations)-1 {
		t.Fatalf("TASK dependency barrier=%v, want every predecessor", task.DependsOn)
	}
	if first.Representations[0].RepresentationDigest == "" || first.Representations[0].RepresentationVersion != 7 {
		t.Fatalf("frozen representation=%+v", first.Representations[0])
	}
	if data, err := CanonicalJSON(first); err != nil || !bytes.Contains(data, []byte(first.PlanDigest)) {
		t.Fatalf("canonical plan: err=%v data=%s", err, data)
	}
}

func TestProjectIntentRejectsCrossImplementProcessEndpoint(t *testing.T) {
	observations := planFixture(t)
	observations[2].Issue = 2
	_, _, err := ProjectIntentForImplement(Intent{Version: 1, BaselineRevision: testBaseline,
		SupersededBy: []IntentEdge{{From: "PROCESS-001", To: "PROCESS-002"}}}, 3, observations)
	if err == nil || !strings.Contains(err.Error(), "both PROCESS identities on the Implement issue") {
		t.Fatalf("cross-Implement edge error=%v", err)
	}
}

func TestCompileBlockerOnlyPlanFailsClosedOnCycle(t *testing.T) {
	input := CompileInput{Repository: "o/r", Proposal: 1, Design: 2, Implement: 3,
		Subject: Subject{PullRequest: 9, URL: "https://github.com/o/r/pull/9", SubjectRevision: testSubject, ProviderBaseRevision: testProviderBase, BaselineRevision: testBaseline, ProviderEvidenceDigest: testEvidence},
		Intent: Intent{Version: 1, BaselineRevision: testBaseline, SupersededBy: []IntentEdge{
			{From: "PROCESS-001", To: "PROCESS-002"}, {From: "PROCESS-002", To: "PROCESS-001"},
		}}, Observations: planFixture(t)}
	plan, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) == 0 || plan.Blockers[0].Code != "supersession-cycle" {
		t.Fatalf("blockers=%+v", plan.Blockers)
	}
	if len(plan.Reconcile.Operations) != 0 {
		t.Fatalf("invalid graph emitted mutations: %+v", plan.Reconcile.Operations)
	}
}

func TestCompileBlockerFreePlanJSONRoundTrip(t *testing.T) {
	plan, err := Compile(CompileInput{Repository: "o/r", Hostname: "github.com", Proposal: 1, Design: 2, Implement: 3,
		Subject:      Subject{PullRequest: 9, URL: "https://github.com/o/r/pull/9", SubjectRevision: testSubject, ProviderBaseRevision: testProviderBase, BaselineRevision: testBaseline, ProviderEvidenceDigest: testEvidence},
		Intent:       Intent{Version: 1, BaselineRevision: testBaseline, SupersededBy: []IntentEdge{{From: "PROCESS-001", To: "PROCESS-002"}}},
		Observations: planFixture(t), LifecycleReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blockers != nil {
		t.Fatalf("blocker-free plan must use canonical nil blockers, got %#v", plan.Blockers)
	}

	data, err := CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"blockers"`)) {
		t.Fatalf("blocker-free plan unexpectedly serialized blockers: %s", data)
	}
	roundTrip, err := ReadPlan(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read blocker-free compiled plan: %v", err)
	}
	if roundTrip.Blockers != nil || roundTrip.PlanDigest != plan.PlanDigest {
		t.Fatalf("round-trip changed blocker representation or digest: blockers=%#v digest=%s want=%s", roundTrip.Blockers, roundTrip.PlanDigest, plan.PlanDigest)
	}
}

func TestCompileNonemptyBlockersRemainCanonicalAndStrict(t *testing.T) {
	plan, err := Compile(CompileInput{Repository: "o/r", Hostname: "github.com", Proposal: 1, Design: 2, Implement: 3,
		Subject:      Subject{PullRequest: 9, URL: "https://github.com/o/r/pull/9", SubjectRevision: testSubject, ProviderBaseRevision: testProviderBase, BaselineRevision: testBaseline, ProviderEvidenceDigest: testEvidence},
		Intent:       Intent{Version: 1, BaselineRevision: testBaseline, SupersededBy: []IntentEdge{{From: "PROCESS-001", To: "PROCESS-002"}}},
		Observations: planFixture(t), LifecycleBlocks: []Blocker{
			{Code: "z-code", ArtifactID: " PROCESS-002 ", Message: " last "},
			{Code: "a-code", ArtifactID: "PROCESS-001", Message: "first"},
			{Code: "a-code", ArtifactID: " PROCESS-001 ", Message: " first "},
			{Code: "", Message: "ignored"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Blocker{
		{Code: "a-code", ArtifactID: "PROCESS-001", Message: "first"},
		{Code: "z-code", ArtifactID: "PROCESS-002", Message: "last"},
	}
	if !reflect.DeepEqual(plan.Blockers, want) {
		t.Fatalf("canonical blockers=%#v, want %#v", plan.Blockers, want)
	}
	data, err := CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPlan(bytes.NewReader(data)); err != nil {
		t.Fatalf("read nonempty canonical blockers: %v", err)
	}

	reordered := plan
	reordered.Blockers = append([]Blocker(nil), plan.Blockers...)
	reordered.Blockers[0], reordered.Blockers[1] = reordered.Blockers[1], reordered.Blockers[0]
	if err := ValidatePlan(reordered); err == nil || !strings.Contains(err.Error(), "blockers are not in canonical order") {
		t.Fatalf("reordered blockers error=%v", err)
	}
	tampered := plan
	tampered.Blockers = append([]Blocker(nil), plan.Blockers...)
	tampered.Blockers[0].Message = "changed"
	if err := ValidatePlan(tampered); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered blocker digest error=%v", err)
	}

	withUnknown := bytes.Replace(data, []byte("\n}\n"), []byte(",\n  \"unexpected\": true\n}\n"), 1)
	if _, err := ReadPlan(bytes.NewReader(withUnknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown plan field error=%v", err)
	}
}

func TestCompileRejectsActualBaselineMismatch(t *testing.T) {
	_, err := Compile(CompileInput{Repository: "o/r", Proposal: 1, Design: 2, Implement: 3,
		Subject: Subject{PullRequest: 9, URL: "https://github.com/o/r/pull/9", SubjectRevision: testSubject,
			ProviderBaseRevision: testProviderBase, BaselineRevision: "3333333333333333333333333333333333333333", ProviderEvidenceDigest: testEvidence},
		Intent: Intent{Version: 1, BaselineRevision: testBaseline}, Observations: planFixture(t)})
	if err == nil || !strings.Contains(err.Error(), "differs from actual baseline") {
		t.Fatalf("error=%v", err)
	}
}

func TestReadIntentRejectsUnknownAndDuplicateEdges(t *testing.T) {
	unknown := `{"version":1,"baseline_revision":"` + testBaseline + `","superseded_by":[],"infer":true}`
	if _, err := ReadIntent(strings.NewReader(unknown)); err == nil {
		t.Fatal("unknown field was accepted")
	}
	duplicate := `{"version":1,"baseline_revision":"` + testBaseline + `","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"},{"from":"PROCESS-001","to":"PROCESS-002"}]}`
	if _, err := ReadIntent(strings.NewReader(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error=%v", err)
	}
}

func planFixture(t *testing.T) []Observation {
	t.Helper()
	taskURL := "https://github.com/o/r/issues/1#issuecomment-10"
	p1URL := "https://github.com/o/r/issues/3#issuecomment-11"
	p2URL := "https://github.com/o/r/issues/3#issuecomment-12"
	task := planBody("TASK", "TASK-001", "confirmed", map[string][]string{"Related Comments": {p1URL, p2URL}}, "task")
	p1 := planBody("PROCESS", "PROCESS-001", "in-progress", map[string][]string{"Related Comments": {taskURL}}, "### Execution Class\n\nchange-bearing")
	p2 := planBody("PROCESS", "PROCESS-002", "in-progress", map[string][]string{"Related Comments": {taskURL}}, "### Execution Class\n\nchange-bearing")
	return []Observation{
		{Issue: 1, CommentID: 10, URL: taskURL, APIURL: "https://api.github.com/repos/o/r/issues/comments/10", Body: task, RepresentationVersion: 7},
		{Issue: 3, CommentID: 11, URL: p1URL, APIURL: "https://api.github.com/repos/o/r/issues/comments/11", Body: p1, RepresentationVersion: 8},
		{Issue: 3, CommentID: 12, URL: p2URL, APIURL: "https://api.github.com/repos/o/r/issues/comments/12", Body: p2, RepresentationVersion: 9},
	}
}

func planBody(typ, id, status string, links map[string][]string, logical string) string {
	return model.RenderMarker(typ, id, 1) + "\n" + model.RenderHeader(typ, id, model.BodyOptions{
		Agent: "Worker", Status: status, Scope: "test", Links: links,
	}) + "\n\n" + logical + "\n"
}
