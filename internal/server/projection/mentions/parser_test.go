package mentions

import (
	"reflect"
	"testing"
)

func TestParserResolvesOnlyGFMProseMentions(t *testing.T) {
	source := []byte("Hello @Alice and @bob, again @alice.\n\n" +
		"`@inline`\n\n```text\n@fenced\n```\n\n    @indented\n\n" +
		"[@label](https://example.test/@target) <https://example.test/@auto> " +
		"https://example.test/@bare user@example.test <mail@example.test>\n")
	got := NewParser().Logins(source)
	want := []string{"alice", "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Logins() = %v, want %v", got, want)
	}
}

func TestParserCanonicalBoundariesAndDeterminism(t *testing.T) {
	got := NewParser().Logins([]byte("/@path x@host @-bad @bad- @good-name @GOOD-name @ok_name @toolong012345678901234567890123456789012345678901234567890123456789"))
	want := []string{"good-name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Logins() = %v, want %v", got, want)
	}
}
