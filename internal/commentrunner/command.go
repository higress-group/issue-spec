package commentrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Kind string

const (
	KindNew    Kind = "new"
	KindResume Kind = "resume"
	KindCancel Kind = "cancel"
)

type Command struct {
	Kind          Kind
	PublicSession string
	Prompt        string
}

func ResolveCancelTarget(cmd Command, activePublicSessionIDs []string) (string, error) {
	if cmd.Kind != KindCancel {
		return "", fmt.Errorf("command is not cancel")
	}
	if cmd.PublicSession != "" {
		return cmd.PublicSession, nil
	}
	if len(activePublicSessionIDs) == 1 {
		return activePublicSessionIDs[0], nil
	}
	return "", fmt.Errorf("bare /cancel is ambiguous; use /cancel <public-session-id>")
}

type FirstObservedComment struct {
	CommentID int64
	Body      string
	Observed  bool
}

func ObserveComment(state FirstObservedComment, body string) FirstObservedComment {
	if state.Observed {
		return state
	}
	state.Observed = true
	state.Body = body
	return state
}

func ParseCommand(body string) (Command, error) {
	first := strings.TrimSpace(firstLine(body))
	if first == "" {
		return Command{}, errors.New("empty command")
	}
	fields := strings.Fields(first)
	if len(fields) == 0 {
		return Command{}, errors.New("empty command")
	}
	switch fields[0] {
	case "/new":
		if len(fields) < 2 {
			return Command{}, errors.New("usage: /new <prompt>")
		}
		return Command{Kind: KindNew, Prompt: strings.TrimSpace(strings.TrimPrefix(first, fields[0]))}, nil
	case "/resume":
		if len(fields) < 3 {
			return Command{}, errors.New("usage: /resume <public-session-id> <prompt>")
		}
		return Command{Kind: KindResume, PublicSession: fields[1], Prompt: strings.TrimSpace(strings.TrimPrefix(first, fields[0]+" "+fields[1]))}, nil
	case "/cancel":
		if len(fields) == 1 {
			return Command{Kind: KindCancel}, nil
		}
		if len(fields) == 2 {
			return Command{Kind: KindCancel, PublicSession: fields[1]}, nil
		}
		return Command{}, errors.New("usage: /cancel <public-session-id>")
	default:
		return Command{}, fmt.Errorf("unsupported command %q", fields[0])
	}
}

func firstLine(body string) string {
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		return body[:i]
	}
	return body
}

type PermissionLookup interface {
	CollaboratorPermission(context.Context, string, string) (string, error)
}

type Allowlist interface {
	Allowed(string) bool
}

func Authorized(ctx context.Context, repo, commenter string, cmd Command, allowlist Allowlist, perms PermissionLookup) error {
	if allowlist != nil && !allowlist.Allowed(commenter) {
		return fmt.Errorf("%s is not configured for issue-spec commands", commenter)
	}
	if perms == nil {
		return errors.New("permission lookup unavailable")
	}
	perm, err := perms.CollaboratorPermission(ctx, repo, commenter)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(perm)) {
	case "write", "maintain", "admin":
		return nil
	default:
		return fmt.Errorf("permission %q is not sufficient", perm)
	}
}

type SessionRepoResolver interface {
	SessionRepo(context.Context, string) (string, error)
}

func AuthorizedForSession(ctx context.Context, sessionRepo string, commenter string, cmd Command, allowlist Allowlist, perms PermissionLookup) error {
	return Authorized(ctx, sessionRepo, commenter, cmd, allowlist, perms)
}
