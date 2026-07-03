package commentrunner

import (
	"strings"
	"testing"
	"time"
)

func TestParseCommandCommentAcceptsNormalizedCommands(t *testing.T) {
	updatedAt := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	observedAt := updatedAt.Add(time.Minute)
	tests := []struct {
		name          string
		body          string
		wantVerb      CommandVerb
		wantPrompt    string
		wantSessionID string
	}{
		{
			name:       "new free form prompt",
			body:       " \n\t/new fix the issue --model ignored-as-prompt\nthen run tests",
			wantVerb:   VerbNew,
			wantPrompt: "fix the issue --model ignored-as-prompt\nthen run tests",
		},
		{
			name:       "new quoted prompt",
			body:       `/new "fix the issue and keep scope narrow"`,
			wantVerb:   VerbNew,
			wantPrompt: "fix the issue and keep scope narrow",
		},
		{
			name:          "resume quoted prompt",
			body:          `/resume sess-ABC_123 "continue the previous turn"`,
			wantVerb:      VerbResume,
			wantPrompt:    "continue the previous turn",
			wantSessionID: "sess-ABC_123",
		},
		{
			name:          "cancel explicit session",
			body:          `/cancel sess.123`,
			wantVerb:      VerbCancel,
			wantSessionID: "sess.123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseCommandComment(TriggerComment{
				Repo:       "o/r",
				Issue:      9,
				CommentID:  101,
				CommentURL: "https://github.com/o/r/issues/9#issuecomment-101",
				Body:       tt.body,
				Commenter:  "alice",
				UpdatedAt:  updatedAt,
				ObservedAt: observedAt,
			})
			if result.Status != ParseStatusAccepted {
				t.Fatalf("status = %s rejection=%+v, want accepted", result.Status, result.Rejection)
			}
			got := result.Candidate
			if got.Verb != tt.wantVerb || got.Prompt != tt.wantPrompt || got.PublicSessionID != tt.wantSessionID {
				t.Fatalf("candidate = %+v", got)
			}
			if got.Repo != "o/r" || got.Issue != 9 || got.TriggerCommentID != 101 || got.Commenter != "alice" {
				t.Fatalf("candidate metadata = %+v", got)
			}
			if !strings.HasPrefix(got.FirstObservedBodyHash, "sha256:") || !strings.HasPrefix(got.IdempotencyKey, "runner-command-v1:") || got.ID == "" {
				t.Fatalf("candidate ids/hashes not populated: %+v", got)
			}
			again := ParseCommandComment(TriggerComment{
				Repo:       "o/r",
				Issue:      9,
				CommentID:  101,
				Body:       tt.body,
				Commenter:  "alice",
				UpdatedAt:  updatedAt,
				ObservedAt: observedAt,
			})
			if again.Candidate.IdempotencyKey != got.IdempotencyKey {
				t.Fatalf("idempotency key changed: %q vs %q", again.Candidate.IdempotencyKey, got.IdempotencyKey)
			}
		})
	}
}

func TestParseCommandCommentRejectsMalformedCommands(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantReason RejectionReason
	}{
		{name: "unknown command", body: "/deploy now", wantReason: ReasonUnknownCommand},
		{name: "new missing prompt", body: "/new", wantReason: ReasonMissingPrompt},
		{name: "resume missing session", body: "/resume", wantReason: ReasonMissingSessionID},
		{name: "resume malformed session slash", body: "/resume bad/id continue", wantReason: ReasonMalformedSessionID},
		{name: "resume malformed session flag", body: "/resume -danger continue", wantReason: ReasonMalformedSessionID},
		{name: "resume missing prompt", body: "/resume sess-123", wantReason: ReasonMissingPrompt},
		{name: "bare cancel rejected", body: "/cancel", wantReason: ReasonBareCancelAmbiguous},
		{name: "cancel extra prompt rejected", body: "/cancel sess-123 please stop", wantReason: ReasonUnexpectedCancelText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseCommandComment(TriggerComment{
				Repo:      "o/r",
				Issue:     1,
				CommentID: 2,
				Body:      tt.body,
				Commenter: "alice",
			})
			if result.Status != ParseStatusRejected || result.Rejection.Reason != tt.wantReason {
				t.Fatalf("result = %+v, want rejection %s", result, tt.wantReason)
			}
		})
	}
}

func TestParseCommandCommentIgnoresNonCommandComments(t *testing.T) {
	for _, body := range []string{
		"",
		"ordinary discussion",
		"ordinary discussion\n/new should not be parsed",
	} {
		result := ParseCommandComment(TriggerComment{Body: body})
		if result.Status != ParseStatusIgnored {
			t.Fatalf("body %q status = %s, want ignored", body, result.Status)
		}
	}
}

func TestParseCommandCommentRejectsInvalidMetadataForCommand(t *testing.T) {
	result := ParseCommandComment(TriggerComment{Body: "/new do work", Commenter: "alice"})
	if result.Status != ParseStatusRejected || result.Rejection.Reason != ReasonInvalidMetadata {
		t.Fatalf("result = %+v, want invalid metadata rejection", result)
	}
}

func TestCommandIdempotencyChangesWithFirstObservedState(t *testing.T) {
	base := TriggerComment{Repo: "o/r", Issue: 1, CommentID: 2, Body: "/new first", Commenter: "alice"}
	first := ParseCommandComment(base)
	edited := base
	edited.Body = "/new edited"
	second := ParseCommandComment(edited)
	if first.Status != ParseStatusAccepted || second.Status != ParseStatusAccepted {
		t.Fatalf("unexpected parse statuses: first=%+v second=%+v", first, second)
	}
	if first.Candidate.IdempotencyKey == second.Candidate.IdempotencyKey {
		t.Fatalf("idempotency key did not change with first observed body state")
	}
}
