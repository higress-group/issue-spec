package commands

import "testing"

func TestIssueClosureRefsFromOptionalPlanningPhases(t *testing.T) {
	refs, err := issueClosureRefsFromFlags("1", "", "3")
	if err != nil || len(refs) != 2 || refs[0].Kind != "proposal" || refs[0].Number != 1 || refs[1].Kind != "implement" || refs[1].Number != 3 {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}
	if _, err := issueClosureRefsFromFlags("", "", ""); err == nil {
		t.Fatal("empty closure selection was accepted")
	}
}
