package model

import (
	"errors"
	"strings"
	"testing"
)

func TestSupersededByRoundTripAndIdempotentStamp(t *testing.T) {
	body, err := EnsureTypedBody("PROCESS", "PROCESS-003", "## Process: old\n\n### Parent TASK\n\n- TASK-001", BodyOptions{Status: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	want := SupersededBy{ProcessID: "PROCESS-017", URL: "https://github.com/o/r/issues/42#issuecomment-123"}
	stamped, changed, err := StampSupersededBy(body, "PROCESS-003", want)
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	if !strings.Contains(stamped, supersededByStart+"\n{\"process_id\":\"PROCESS-017\",\"url\":\"https://github.com/o/r/issues/42#issuecomment-123\"}\n"+supersededByEnd) {
		t.Fatalf("marker is not canonical:\n%s", stamped)
	}
	got, found, err := ParseSupersededBy(stamped, "PROCESS-003")
	if err != nil || !found || got != want {
		t.Fatalf("got=%+v found=%t err=%v", got, found, err)
	}
	again, changed, err := StampSupersededBy(stamped, "PROCESS-003", want)
	if err != nil || changed || again != stamped {
		t.Fatalf("idempotent stamp changed=%t err=%v", changed, err)
	}
}

func TestSupersededByRejectsMalformedDuplicateAndConflictingAuthority(t *testing.T) {
	valid := supersededByStart + "\n{\"process_id\":\"PROCESS-017\",\"url\":\"https://example.test/issues/1#comment-17\"}\n" + supersededByEnd
	for name, body := range map[string]string{
		"noncompact":    strings.Replace(valid, `,"url"`, `, "url"`, 1),
		"field order":   strings.Replace(valid, "{\"process_id\":\"PROCESS-017\",\"url\":\"https://example.test/issues/1#comment-17\"}", "{\"url\":\"https://example.test/issues/1#comment-17\",\"process_id\":\"PROCESS-017\"}", 1),
		"missing id":    strings.Replace(valid, "\"PROCESS-017\"", "\"\"", 1),
		"self link":     strings.Replace(valid, "\"PROCESS-017\"", "\"PROCESS-003\"", 1),
		"duplicate":     valid + "\n" + valid,
		"wrong version": strings.Replace(valid, "version=1", "version=2", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, found, err := ParseSupersededBy(body, "PROCESS-003"); !found || err == nil {
				t.Fatalf("found=%t err=%v", found, err)
			}
		})
	}

	body, err := EnsureTypedBody("PROCESS", "PROCESS-003", "## Process: old\n\n### Parent TASK\n\n- TASK-001", BodyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := SupersededBy{ProcessID: "PROCESS-017", URL: "https://example.test/issues/1#comment-17"}
	body, _, err = StampSupersededBy(body, "PROCESS-003", first)
	if err != nil {
		t.Fatal(err)
	}
	second := SupersededBy{ProcessID: "PROCESS-018", URL: "https://example.test/issues/1#comment-18"}
	if _, _, err := StampSupersededBy(body, "PROCESS-003", second); !errors.Is(err, ErrSupersededByConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}
