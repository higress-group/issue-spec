package acpx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Mode string

const (
	ModeFresh  Mode = "fresh"
	ModeEnsure Mode = "ensure"
	ModeResume Mode = "resume"
)

type Kind string

const (
	KindCodex  Kind = "codex"
	KindClaude Kind = "claude"
)

type PromptInput struct {
	Stdin []byte
	Temp  string
}

type SessionRequest struct {
	Mode         Mode
	Kind         Kind
	Agent        string
	Model        string
	SessionID    string
	Prompt       string
	PromptInput  PromptInput
	Queue        bool
	NoWait       bool
	RefreshMeta  bool
	MetadataPath string
}

type SessionResult struct {
	SessionID string
	Queued    bool
	Metadata  map[string]string
	RawOutput string
}

type CapabilityProbe struct {
	SupportsReconcile           bool
	SupportsTurnCancellation    bool
	SupportsTurnCancellationErr error
}

type Runner interface {
	Run(context.Context, Command) (Result, error)
}

type Command struct {
	Binary string
	Args   []string
	Stdin  []byte
	Env    []string
	Dir    string
}

type Result struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

type Adapter struct {
	Binary string
	Runner Runner
	Lookup func(string) (string, error)
}

func (a Adapter) resolveBinary() (string, error) {
	bin := strings.TrimSpace(a.Binary)
	if bin == "" {
		return "", fmt.Errorf("acpx binary is required")
	}
	if a.Lookup != nil {
		return a.Lookup(bin)
	}
	return exec.LookPath(bin)
}

func (a Adapter) Run(ctx context.Context, req SessionRequest) (SessionResult, error) {
	if a.Runner == nil {
		return SessionResult{}, fmt.Errorf("acpx runner is not configured")
	}
	bin, err := a.resolveBinary()
	if err != nil {
		return SessionResult{}, err
	}
	args, stdin, err := buildArgs(req)
	if err != nil {
		return SessionResult{}, err
	}
	res, err := a.Runner.Run(ctx, Command{Binary: bin, Args: args, Stdin: stdin})
	if err != nil {
		return SessionResult{}, err
	}
	return parseResult(res)
}

func (a Adapter) ProbeCapabilities(ctx context.Context) (CapabilityProbe, error) {
	bin, err := a.resolveBinary()
	if err != nil {
		return CapabilityProbe{}, err
	}
	if a.Runner == nil {
		return CapabilityProbe{}, fmt.Errorf("acpx runner is not configured")
	}
	res, err := a.Runner.Run(ctx, Command{Binary: bin, Args: []string{"probe", "--capabilities"}})
	if err != nil {
		return CapabilityProbe{}, err
	}
	return parseCapabilities(res)
}

func buildArgs(req SessionRequest) ([]string, []byte, error) {
	var args []string
	switch req.Mode {
	case ModeFresh, "":
		args = append(args, "new")
	case ModeEnsure:
		args = append(args, "ensure")
	case ModeResume:
		if strings.TrimSpace(req.SessionID) == "" {
			return nil, nil, fmt.Errorf("resume session id is required")
		}
		args = append(args, "resume", req.SessionID)
	default:
		return nil, nil, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	switch req.Kind {
	case KindCodex, "":
		args = append(args, "--codex")
	case KindClaude:
		args = append(args, "--claude")
	default:
		return nil, nil, fmt.Errorf("unsupported kind %q", req.Kind)
	}
	if strings.TrimSpace(req.Agent) != "" {
		args = append(args, "--agent", strings.TrimSpace(req.Agent))
	}
	if strings.TrimSpace(req.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(req.Model))
	}
	if req.Queue {
		args = append(args, "--queue")
	}
	if req.NoWait {
		args = append(args, "--no-wait")
	}
	if req.RefreshMeta {
		args = append(args, "--refresh-meta")
	}
	if strings.TrimSpace(req.MetadataPath) != "" {
		args = append(args, "--metadata-path", strings.TrimSpace(req.MetadataPath))
	}
	if strings.TrimSpace(req.Prompt) != "" {
		args = append(args, "--prompt", req.Prompt)
	}
	if len(req.PromptInput.Stdin) > 0 {
		return args, append([]byte(nil), req.PromptInput.Stdin...), nil
	}
	if strings.TrimSpace(req.PromptInput.Temp) != "" {
		args = append(args, "--prompt-file", strings.TrimSpace(req.PromptInput.Temp))
	}
	return args, nil, nil
}

func parseResult(res Result) (SessionResult, error) {
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return SessionResult{}, fmt.Errorf("acpx returned empty output")
	}
	if strings.Contains(out, "resume-mismatch") {
		return SessionResult{}, fmt.Errorf("resume mismatch: %s", out)
	}
	if strings.Contains(out, "malformed-output") {
		return SessionResult{}, fmt.Errorf("malformed acpx output: %s", out)
	}
	result := SessionResult{RawOutput: out}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "session_id="):
			result.SessionID = strings.TrimSpace(strings.TrimPrefix(line, "session_id="))
		case strings.HasPrefix(line, "queued="):
			result.Queued = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "queued=")), "true")
		case strings.HasPrefix(line, "metadata."):
			if result.Metadata == nil {
				result.Metadata = map[string]string{}
			}
			kv := strings.SplitN(strings.TrimPrefix(line, "metadata."), "=", 2)
			if len(kv) == 2 {
				result.Metadata[kv[0]] = kv[1]
			}
		}
	}
	if result.SessionID == "" {
		return SessionResult{}, fmt.Errorf("malformed acpx output: missing session_id")
	}
	return result, nil
}

func parseCapabilities(res Result) (CapabilityProbe, error) {
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		return CapabilityProbe{}, fmt.Errorf("empty capability output")
	}
	probe := CapabilityProbe{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "reconcile=true":
			probe.SupportsReconcile = true
		case "reconcile=false":
			probe.SupportsReconcile = false
		case "turn_cancel=true":
			probe.SupportsTurnCancellation = true
		case "turn_cancel=false":
			probe.SupportsTurnCancellation = false
			probe.SupportsTurnCancellationErr = errors.New("turn-level cancellation unsupported")
		}
	}
	return probe, nil
}
