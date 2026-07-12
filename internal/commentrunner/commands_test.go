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
		wantProcessID string
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
			name:       "new whole quoted process text stays prompt",
			body:       `/new "document --process PROCESS-999 as literal text"`,
			wantVerb:   VerbNew,
			wantPrompt: "document --process PROCESS-999 as literal text",
		},
		{
			name:       "new process substring without token boundary stays prompt",
			body:       `/new document foo--processbar as literal text`,
			wantVerb:   VerbNew,
			wantPrompt: "document foo--processbar as literal text",
		},
		{
			name:          "resume quoted prompt",
			body:          `/resume sess-ABC_123 --process PROCESS-008 "continue --process PROCESS-999 as text"`,
			wantVerb:      VerbResume,
			wantPrompt:    "continue --process PROCESS-999 as text",
			wantSessionID: "sess-ABC_123",
			wantProcessID: "PROCESS-008",
		},
		{
			name:          "resume single quoted process text stays prompt",
			body:          `/resume sess-ABC_123 --process PROCESS-008 'continue --process PROCESS-999 as text'`,
			wantVerb:      VerbResume,
			wantPrompt:    "continue --process PROCESS-999 as text",
			wantSessionID: "sess-ABC_123",
			wantProcessID: "PROCESS-008",
		},
		{
			name:          "resume escaped quote keeps process text quoted",
			body:          `/resume sess-ABC_123 --process PROCESS-008 "say \"--process PROCESS-999\" as text"`,
			wantVerb:      VerbResume,
			wantPrompt:    `say "--process PROCESS-999" as text`,
			wantSessionID: "sess-ABC_123",
			wantProcessID: "PROCESS-008",
		},
		{
			name:          "ordinary unmatched quote remains prompt",
			body:          `/resume sess-ABC_123 explain "ordinary text`,
			wantVerb:      VerbResume,
			wantPrompt:    `explain "ordinary text`,
			wantSessionID: "sess-ABC_123",
		},
		{
			name:          "resume orchestration without process",
			body:          `/resume sess-ABC_123 continue the previous turn`,
			wantVerb:      VerbResume,
			wantPrompt:    "continue the previous turn",
			wantSessionID: "sess-ABC_123",
		},
		{
			name:          "resume escaped process prefix stays prompt",
			body:          `/resume sess-ABC_123 \--process PROCESS-999 as literal text`,
			wantVerb:      VerbResume,
			wantPrompt:    `\--process PROCESS-999 as literal text`,
			wantSessionID: "sess-ABC_123",
		},
		{
			name:          "resume quoted process prefix stays prompt",
			body:          `/resume sess-ABC_123 "--process" PROCESS-999 as literal text`,
			wantVerb:      VerbResume,
			wantPrompt:    `"--process" PROCESS-999 as literal text`,
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
			if got.Verb != tt.wantVerb || got.Prompt != tt.wantPrompt || got.PublicSessionID != tt.wantSessionID || got.ExactProcessID != tt.wantProcessID {
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
		{name: "resume missing process id", body: "/resume sess-123 --process", wantReason: ReasonMissingProcessID},
		{name: "resume malformed process id", body: "/resume sess-123 --process PROCESS-8 work", wantReason: ReasonMalformedProcessID},
		{name: "resume duplicate process flag", body: "/resume sess-123 --process PROCESS-008 --process PROCESS-009 work", wantReason: ReasonDuplicateProcessFlag},
		{name: "resume duplicate after closed quote with trailing quote", body: `/resume sess-123 --process PROCESS-008 "safe" --process PROCESS-999 "`, wantReason: ReasonDuplicateProcessFlag},
		{name: "resume misplaced process flag", body: "/resume sess-123 work --process PROCESS-008", wantReason: ReasonUnexpectedProcessFlag},
		{name: "resume misplaced after closed single quote", body: `/resume sess-123 'safe' --process PROCESS-999`, wantReason: ReasonUnexpectedProcessFlag},
		{name: "resume malformed flag-like token", body: "/resume sess-123 work --process=PROCESS-008", wantReason: ReasonMalformedProcessID},
		{name: "resume unquoted prefix with escaped suffix", body: `/resume sess-123 work --process\\`, wantReason: ReasonMalformedProcessID},
		{name: "resume unclosed quote around process text", body: `/resume sess-123 "safe --process PROCESS-999`, wantReason: ReasonMalformedCommandSyntax},
		{name: "resume excess quote around process text", body: `/resume sess-123 "safe"--process PROCESS-999`, wantReason: ReasonMalformedCommandSyntax},
		{name: "resume invalid escape around process text", body: `/resume sess-123 "safe \q --process PROCESS-999"`, wantReason: ReasonMalformedCommandSyntax},
		{name: "new process rejected", body: "/new --process PROCESS-008 work", wantReason: ReasonUnexpectedProcessFlag},
		{name: "new unquoted prefix with escaped suffix", body: `/new work --process\\`, wantReason: ReasonUnexpectedProcessFlag},
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

func TestCommandIdempotencyExplicitlyIncludesExactProcess(t *testing.T) {
	comment := TriggerComment{Repo: "o/r", Issue: 1, CommentID: 2, Commenter: "alice"}
	bodyHash := BodyHash("same normalized body")
	first := commandIdempotencyKey(comment, VerbResume, "sess-1", "PROCESS-008", bodyHash)
	second := commandIdempotencyKey(comment, VerbResume, "sess-1", "PROCESS-009", bodyHash)
	if first == second {
		t.Fatal("exact PROCESS id did not affect idempotency")
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

func TestCommandIdempotencyIsStableAcrossObservationTime(t *testing.T) {
	updatedAt := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	base := TriggerComment{Repo: "o/r", Issue: 1, CommentID: 2, Body: "/new first", Commenter: "alice", UpdatedAt: updatedAt, ObservedAt: updatedAt.Add(time.Minute)}
	first := ParseCommandComment(base)
	redelivered := base
	redelivered.ObservedAt = updatedAt.Add(2 * time.Minute)
	second := ParseCommandComment(redelivered)
	if first.Status != ParseStatusAccepted || second.Status != ParseStatusAccepted {
		t.Fatalf("unexpected parse statuses: first=%+v second=%+v", first, second)
	}
	if first.Candidate.IdempotencyKey != second.Candidate.IdempotencyKey {
		t.Fatalf("idempotency key changed with observation time: %q vs %q", first.Candidate.IdempotencyKey, second.Candidate.IdempotencyKey)
	}
}

func TestCommandIdempotencyChangesWithUpdatedCommandBody(t *testing.T) {
	updatedAt := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	base := TriggerComment{Repo: "o/r", Issue: 1, CommentID: 2, Body: "/new first", Commenter: "alice", UpdatedAt: updatedAt, ObservedAt: updatedAt.Add(time.Minute)}
	first := ParseCommandComment(base)
	edited := base
	edited.Body = "/new edited"
	edited.UpdatedAt = updatedAt.Add(time.Minute)
	second := ParseCommandComment(edited)
	if first.Status != ParseStatusAccepted || second.Status != ParseStatusAccepted {
		t.Fatalf("unexpected parse statuses: first=%+v second=%+v", first, second)
	}
	if first.Candidate.IdempotencyKey == second.Candidate.IdempotencyKey {
		t.Fatalf("idempotency key did not change with updated command body")
	}
}
