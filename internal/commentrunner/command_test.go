package commentrunner

import (
	"context"
	"errors"
	"testing"
)

type fakeAllowlist map[string]bool

func (f fakeAllowlist) Allowed(login string) bool { return f[login] }

type fakePerms struct {
	perm string
	err  error
	repo string
	user string
}

func (f *fakePerms) CollaboratorPermission(_ context.Context, repo, user string) (string, error) {
	f.repo, f.user = repo, user
	return f.perm, f.err
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Command
		ok   bool
	}{
		{name: "new", body: "/new do thing", want: Command{Kind: KindNew, Prompt: "do thing"}, ok: true},
		{name: "resume", body: "/resume sess-1 keep going", want: Command{Kind: KindResume, PublicSession: "sess-1", Prompt: "keep going"}, ok: true},
		{name: "cancel", body: "/cancel sess-1", want: Command{Kind: KindCancel, PublicSession: "sess-1"}, ok: true},
		{name: "bare cancel", body: "/cancel", want: Command{Kind: KindCancel}, ok: true},
		{name: "invalid", body: "/bogus hi", ok: false},
		{name: "flag injection ignored", body: "/new prompt --model foo", want: Command{Kind: KindNew, Prompt: "prompt --model foo"}, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommand(tt.body)
			if tt.ok && err != nil {
				t.Fatal(err)
			}
			if !tt.ok && err == nil {
				t.Fatal("ParseCommand succeeded, want error")
			}
			if got != tt.want {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}

func TestObserveCommentKeepsFirstBody(t *testing.T) {
	state := ObserveComment(FirstObservedComment{CommentID: 7}, "/new first")
	state = ObserveComment(state, "/new edited")
	if !state.Observed || state.Body != "/new first" {
		t.Fatalf("state = %+v", state)
	}
}

func TestAuthorized(t *testing.T) {
	cmd := Command{Kind: KindNew}
	perms := &fakePerms{perm: "write"}
	if err := Authorized(context.Background(), "o/r", "alice", cmd, fakeAllowlist{"alice": true}, perms); err != nil {
		t.Fatal(err)
	}
	if perms.repo != "o/r" || perms.user != "alice" {
		t.Fatalf("permission lookup = %s/%s", perms.repo, perms.user)
	}
}

func TestAuthorizedRejectsUnauthorizedAndUnknownPermissions(t *testing.T) {
	cmd := Command{Kind: KindResume}
	if err := Authorized(context.Background(), "o/r", "mallory", cmd, fakeAllowlist{"alice": true}, &fakePerms{perm: "write"}); err == nil {
		t.Fatal("allowlist bypassed")
	}
	for _, perm := range []string{"read", "", "triage"} {
		if err := Authorized(context.Background(), "o/r", "alice", cmd, fakeAllowlist{"alice": true}, &fakePerms{perm: perm}); err == nil {
			t.Fatalf("permission %q accepted", perm)
		}
	}
	if err := Authorized(context.Background(), "o/r", "alice", cmd, fakeAllowlist{"alice": true}, &fakePerms{err: errors.New("lookup failed")}); err == nil {
		t.Fatal("lookup failure accepted")
	}
}

func TestResolveCancelTarget(t *testing.T) {
	got, err := ResolveCancelTarget(Command{Kind: KindCancel}, []string{"sess-1"})
	if err != nil || got != "sess-1" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := ResolveCancelTarget(Command{Kind: KindCancel}, nil); err == nil {
		t.Fatal("bare cancel without one active session accepted")
	}
	if _, err := ResolveCancelTarget(Command{Kind: KindCancel, PublicSession: "sess-2"}, []string{"sess-1"}); err != nil {
		t.Fatal(err)
	}
}
