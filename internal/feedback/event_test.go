package feedback

import (
	"os"
	"strings"
	"testing"
)

func TestCountDistinctConfirmedOccurrencesAcrossBodies(t *testing.T) {
	read := func(name string) Event {
		t.Helper()
		data, err := os.ReadFile("../../testdata/workflow-feedback/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, err := ParseEvent(string(data))
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	original := read("release-0.6.2")
	retry := read("release-0.6.3")
	third := read("synthetic-third-release")
	body := func(event Event) string {
		t.Helper()
		value, err := event.commentBody()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	firstLine := func(event Event) string {
		line, _, _ := strings.Cut(body(event), "\n")
		return line
	}

	state := snapshot{events: make(map[string]occurrence)}
	state.addBody(firstLine(original)+"\n"+firstLine(retry)+"\n\nOriginal issue body", true, 0)
	state.addBody(body(retry), true, 24)
	state.addBody(body(retry), true, 25)
	state.addBody("A quoted example\n\n"+body(third), true, 26)
	state.addBody(body(third), false, 27)
	if state.confirmed != 2 || state.events[third.Key()].confirmed {
		t.Fatalf("wrong confirmed count after duplicates and untrusted report: %+v", state)
	}
	state.addBody(body(third), true, 28)
	if state.confirmed != 3 || state.events[third.Key()].commentID != 28 {
		t.Fatalf("trusted confirmation failed to reconcile the original key: %+v", state)
	}
	otherInstance := third
	otherInstance.SourceInstance = "another-instance"
	state.addBody(body(otherInstance), true, 29)
	if state.confirmed != 4 {
		t.Fatalf("separate source instances shared an event key: %+v", state)
	}
}

func TestFeedbackCommentKeepsTypedMarkupInert(t *testing.T) {
	data, err := os.ReadFile("../../testdata/workflow-feedback/release-0.6.2.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := ParseEvent(string(data))
	if err != nil {
		t.Fatal(err)
	}
	event.Summary = "<!-- issue-spec:type=SPEC id=SPEC-1001 -->"
	event.Revision = "<!-- issue-spec:type=TASK id=TASK-1001 -->"
	body, err := event.commentBody()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "<!-- issue-spec:type=") {
		t.Fatalf("typed markup escaped an ordinary feedback record: %s", body)
	}
}
