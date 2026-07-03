package acpx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAdapterMissingBinary(t *testing.T) {
	a := Adapter{Binary: "", Runner: &fakeRunner{}}
	_, err := a.Run(context.Background(), SessionRequest{Mode: ModeFresh, Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "binary is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestAdapterQueueAndPromptInput(t *testing.T) {
	r := &fakeRunner{result: Result{Stdout: []byte("session_id=s-1\nqueued=true\nmetadata.rev=7\n")}}
	a := Adapter{Binary: "acpx", Lookup: func(string) (string, error) { return "/bin/acpx", nil }, Runner: r}
	got, err := a.Run(context.Background(), SessionRequest{
		Mode:        ModeFresh,
		Kind:        KindClaude,
		Agent:       "coder",
		Model:       "sonnet",
		Queue:       true,
		NoWait:      true,
		RefreshMeta: true,
		PromptInput: PromptInput{Stdin: []byte("prompt body")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Queued || got.SessionID != "s-1" || got.Metadata["rev"] != "7" {
		t.Fatalf("result = %#v", got)
	}
	if len(r.commands) != 1 {
		t.Fatalf("commands = %#v", r.commands)
	}
	cmd := r.commands[0]
	for _, want := range []string{"new", "--claude", "--agent", "coder", "--model", "sonnet", "--queue", "--no-wait", "--refresh-meta"} {
		if !contains(cmd.Args, want) {
			t.Fatalf("missing %q in %#v", want, cmd.Args)
		}
	}
	if string(cmd.Stdin) != "prompt body" {
		t.Fatalf("stdin = %q", string(cmd.Stdin))
	}
}

func TestAdapterResumeMismatch(t *testing.T) {
	a := Adapter{Binary: "acpx", Lookup: func(string) (string, error) { return "/bin/acpx", nil }, Runner: &fakeRunner{result: Result{Stdout: []byte("resume-mismatch: session drift")}}}
	_, err := a.Run(context.Background(), SessionRequest{Mode: ModeResume, SessionID: "s-1"})
	if err == nil || !strings.Contains(err.Error(), "resume mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestAdapterMalformedOutput(t *testing.T) {
	a := Adapter{Binary: "acpx", Lookup: func(string) (string, error) { return "/bin/acpx", nil }, Runner: &fakeRunner{result: Result{Stdout: []byte("queued=true")}}}
	_, err := a.Run(context.Background(), SessionRequest{Mode: ModeEnsure})
	if err == nil || !strings.Contains(err.Error(), "missing session_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeCapabilities(t *testing.T) {
	r := &fakeRunner{result: Result{Stdout: []byte("reconcile=true\nturn_cancel=false\n")}}
	a := Adapter{Binary: "acpx", Lookup: func(string) (string, error) { return "/bin/acpx", nil }, Runner: r}
	got, err := a.ProbeCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.SupportsReconcile {
		t.Fatal("expected reconcile support")
	}
	if got.SupportsTurnCancellation {
		t.Fatal("expected turn cancellation unsupported")
	}
	if got.SupportsTurnCancellationErr == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestProbeMissingBinary(t *testing.T) {
	a := Adapter{Binary: "missing", Lookup: func(string) (string, error) { return "", errors.New("not found") }, Runner: &fakeRunner{}}
	_, err := a.ProbeCapabilities(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

type fakeRunner struct {
	commands []Command
	result   Result
}

func (f *fakeRunner) Run(_ context.Context, cmd Command) (Result, error) {
	f.commands = append(f.commands, cmd)
	return f.result, nil
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
