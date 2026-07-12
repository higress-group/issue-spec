package commentrunner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type CommandVerb string

const (
	VerbNew    CommandVerb = "new"
	VerbResume CommandVerb = "resume"
	VerbCancel CommandVerb = "cancel"
)

type ParseStatus string

const (
	ParseStatusIgnored  ParseStatus = "ignored"
	ParseStatusAccepted ParseStatus = "accepted"
	ParseStatusRejected ParseStatus = "rejected"
)

type RejectionReason string

const (
	ReasonUnknownCommand         RejectionReason = "unknown_command"
	ReasonMissingPrompt          RejectionReason = "missing_prompt"
	ReasonMissingSessionID       RejectionReason = "missing_session_id"
	ReasonMalformedSessionID     RejectionReason = "malformed_session_id"
	ReasonBareCancelAmbiguous    RejectionReason = "bare_cancel_ambiguous"
	ReasonUnexpectedCancelText   RejectionReason = "unexpected_cancel_text"
	ReasonMissingProcessID       RejectionReason = "missing_process_id"
	ReasonMalformedProcessID     RejectionReason = "malformed_process_id"
	ReasonDuplicateProcessFlag   RejectionReason = "duplicate_process_flag"
	ReasonUnexpectedProcessFlag  RejectionReason = "unexpected_process_flag"
	ReasonMalformedCommandSyntax RejectionReason = "malformed_command_syntax"
	ReasonInvalidMetadata        RejectionReason = "invalid_metadata"
)

type TriggerComment struct {
	Repo       string
	Issue      int
	CommentID  int64
	CommentURL string
	Body       string
	Commenter  string
	UpdatedAt  time.Time
	ObservedAt time.Time
}

type CommandCandidate struct {
	ID                     string      `json:"id"`
	IdempotencyKey         string      `json:"idempotency_key"`
	Repo                   string      `json:"repo"`
	Issue                  int         `json:"issue"`
	TriggerCommentID       int64       `json:"trigger_comment_id"`
	CommentURL             string      `json:"comment_url,omitempty"`
	FirstObservedAt        time.Time   `json:"first_observed_at,omitempty"`
	FirstObservedUpdatedAt time.Time   `json:"first_observed_updated_at,omitempty"`
	FirstObservedBodyHash  string      `json:"first_observed_body_hash"`
	Commenter              string      `json:"commenter"`
	Verb                   CommandVerb `json:"verb"`
	Prompt                 string      `json:"prompt,omitempty"`
	PublicSessionID        string      `json:"public_session_id,omitempty"`
	ExactProcessID         string      `json:"exact_process_id,omitempty"`
}

type CommandRejection struct {
	Reason  RejectionReason `json:"reason"`
	Message string          `json:"message"`
}

type ParseResult struct {
	Status    ParseStatus      `json:"status"`
	Candidate CommandCandidate `json:"candidate,omitempty"`
	Rejection CommandRejection `json:"rejection,omitempty"`
}

var (
	publicSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
	exactProcessIDPattern  = regexp.MustCompile(`^PROCESS-[0-9]{3,}$`)
)

func ParseCommandComment(comment TriggerComment) ParseResult {
	body := strings.TrimLeftFunc(comment.Body, unicode.IsSpace)
	if body == "" || !strings.HasPrefix(body, "/") {
		return ParseResult{Status: ParseStatusIgnored}
	}

	token, rest := splitToken(body)
	switch token {
	case "/new":
		flags := scanProcessFlagTokens(rest)
		if flags.hasFlagLike() {
			return rejected(ReasonUnexpectedProcessFlag, "/new does not accept --process")
		}
		if flags.MalformedSyntax {
			return rejected(ReasonMalformedCommandSyntax, "malformed quoting or escaping around --process text")
		}
		prompt := normalizePromptTail(rest)
		if prompt == "" {
			return rejected(ReasonMissingPrompt, "/new requires prompt text")
		}
		return accepted(comment, VerbNew, "", "", prompt)
	case "/resume":
		sessionID, promptTail := splitToken(strings.TrimLeftFunc(rest, unicode.IsSpace))
		if sessionID == "" {
			return rejected(ReasonMissingSessionID, "/resume requires a public session id")
		}
		if err := ValidatePublicSessionID(sessionID); err != nil {
			return rejected(ReasonMalformedSessionID, err.Error())
		}
		exactProcessID := ""
		first, afterFirst := splitToken(strings.TrimLeftFunc(promptTail, unicode.IsSpace))
		if first == "--process" {
			exactProcessID, promptTail = splitToken(strings.TrimLeftFunc(afterFirst, unicode.IsSpace))
			if exactProcessID == "" {
				return rejected(ReasonMissingProcessID, "--process requires an exact PROCESS id")
			}
			if err := ValidateExactProcessID(exactProcessID); err != nil {
				return rejected(ReasonMalformedProcessID, err.Error())
			}
			flags := scanProcessFlagTokens(promptTail)
			if flags.Exact > 0 {
				return rejected(ReasonDuplicateProcessFlag, "--process may appear only once before the prompt")
			}
			if flags.Malformed > 0 {
				return rejected(ReasonMalformedProcessID, "malformed --process flag")
			}
			if flags.MalformedSyntax {
				return rejected(ReasonMalformedCommandSyntax, "malformed quoting or escaping around --process text")
			}
		} else {
			flags := scanProcessFlagTokens(promptTail)
			if flags.Malformed > 0 {
				return rejected(ReasonMalformedProcessID, "malformed --process flag")
			}
			if flags.Exact > 0 {
				return rejected(ReasonUnexpectedProcessFlag, "--process must immediately follow the public session id")
			}
			if flags.MalformedSyntax {
				return rejected(ReasonMalformedCommandSyntax, "malformed quoting or escaping around --process text")
			}
		}
		prompt := normalizePromptTail(promptTail)
		if prompt == "" {
			return rejected(ReasonMissingPrompt, "/resume requires prompt text")
		}
		return accepted(comment, VerbResume, sessionID, exactProcessID, prompt)
	case "/cancel":
		sessionID, extra := splitToken(strings.TrimLeftFunc(rest, unicode.IsSpace))
		if sessionID == "" {
			return rejected(ReasonBareCancelAmbiguous, "bare /cancel is ambiguous; use /cancel <public-session-id>")
		}
		if err := ValidatePublicSessionID(sessionID); err != nil {
			return rejected(ReasonMalformedSessionID, err.Error())
		}
		if strings.TrimSpace(extra) != "" {
			return rejected(ReasonUnexpectedCancelText, "/cancel accepts only a public session id")
		}
		return accepted(comment, VerbCancel, sessionID, "", "")
	default:
		return rejected(ReasonUnknownCommand, fmt.Sprintf("unsupported runner command %q", strings.TrimPrefix(token, "/")))
	}
}

func ValidateExactProcessID(id string) error {
	if id != strings.TrimSpace(id) || !exactProcessIDPattern.MatchString(id) {
		return fmt.Errorf("malformed exact PROCESS id %q", id)
	}
	return nil
}

func ValidatePublicSessionID(id string) error {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return fmt.Errorf("public session id is empty")
	case !publicSessionIDPattern.MatchString(id):
		return fmt.Errorf("malformed public session id %q", id)
	default:
		return nil
	}
}

func BodyHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func accepted(comment TriggerComment, verb CommandVerb, sessionID, exactProcessID, prompt string) ParseResult {
	if strings.TrimSpace(comment.Repo) == "" || comment.Issue <= 0 || comment.CommentID <= 0 || strings.TrimSpace(comment.Commenter) == "" {
		return rejected(ReasonInvalidMetadata, "command candidate is missing repo, issue, comment id, or commenter")
	}

	bodyHash := BodyHash(comment.Body)
	idempotencyKey := commandIdempotencyKey(comment, verb, sessionID, exactProcessID, bodyHash)
	return ParseResult{
		Status: ParseStatusAccepted,
		Candidate: CommandCandidate{
			ID:                     "cmd-" + shortHash(idempotencyKey),
			IdempotencyKey:         idempotencyKey,
			Repo:                   strings.TrimSpace(comment.Repo),
			Issue:                  comment.Issue,
			TriggerCommentID:       comment.CommentID,
			CommentURL:             strings.TrimSpace(comment.CommentURL),
			FirstObservedAt:        comment.ObservedAt.UTC(),
			FirstObservedUpdatedAt: comment.UpdatedAt.UTC(),
			FirstObservedBodyHash:  bodyHash,
			Commenter:              strings.TrimSpace(comment.Commenter),
			Verb:                   verb,
			Prompt:                 prompt,
			PublicSessionID:        sessionID,
			ExactProcessID:         exactProcessID,
		},
	}
}

type processFlagScan struct {
	Exact           int
	Malformed       int
	MalformedSyntax bool
}

func (s processFlagScan) hasFlagLike() bool {
	return s.Exact > 0 || s.Malformed > 0
}

// scanProcessFlagTokens performs one quote- and escape-aware pass over a prompt
// tail. Only a token whose --process prefix is wholly unquoted and unescaped is
// classified as a flag. Quoted prompt text remains opaque, while malformed
// quoting or escaping around flag-like text fails closed instead of concealing
// a flag. Ordinary prompts without --process text keep normalizePromptTail's
// existing permissive quote semantics.
func scanProcessFlagTokens(value string) processFlagScan {
	var result processFlagScan
	var token strings.Builder
	var quotedRaw strings.Builder
	var quote rune
	prefixProtected := false
	tokenRunes := 0
	escaped := false
	justClosedQuote := false
	sawFlagText := strings.Contains(value, "--process")

	flush := func() {
		candidate := token.String()
		if !prefixProtected && strings.HasPrefix(candidate, "--process") {
			if candidate == "--process" {
				result.Exact++
			} else {
				result.Malformed++
			}
		}
		token.Reset()
		prefixProtected = false
		tokenRunes = 0
		justClosedQuote = false
	}
	writeToken := func(r rune, protected bool) {
		if tokenRunes < len("--process") && protected {
			prefixProtected = true
		}
		tokenRunes++
		token.WriteRune(r)
	}

	for _, r := range value {
		if quote != 0 {
			quotedRaw.WriteRune(r)
			if escaped {
				writeToken(r, true)
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == quote {
				if quote == '"' {
					if _, err := strconv.Unquote(quotedRaw.String()); err != nil {
						result.MalformedSyntax = true
					}
				}
				quote = 0
				justClosedQuote = true
				continue
			}
			writeToken(r, true)
			continue
		}

		if escaped {
			writeToken(r, true)
			escaped = false
			justClosedQuote = false
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if justClosedQuote {
			result.MalformedSyntax = true
		}
		switch r {
		case '\\':
			escaped = true
		case '"', '\'':
			if token.Len() > 0 {
				result.MalformedSyntax = true
			}
			quote = r
			quotedRaw.Reset()
			quotedRaw.WriteRune(r)
		default:
			writeToken(r, false)
		}
	}
	if escaped || quote != 0 {
		result.MalformedSyntax = true
	}
	flush()
	if !sawFlagText {
		result.MalformedSyntax = false
	}
	return result
}

func rejected(reason RejectionReason, message string) ParseResult {
	return ParseResult{
		Status:    ParseStatusRejected,
		Rejection: CommandRejection{Reason: reason, Message: message},
	}
}

func splitToken(value string) (string, string) {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if value == "" {
		return "", ""
	}
	for i, r := range value {
		if unicode.IsSpace(r) {
			return value[:i], value[i:]
		}
	}
	return value, ""
}

func normalizePromptTail(value string) string {
	prompt := strings.TrimSpace(value)
	if len(prompt) < 2 {
		return prompt
	}
	switch {
	case prompt[0] == '"' && prompt[len(prompt)-1] == '"':
		if unquoted, err := strconv.Unquote(prompt); err == nil {
			return unquoted
		}
	case prompt[0] == '\'' && prompt[len(prompt)-1] == '\'':
		return strings.ReplaceAll(prompt[1:len(prompt)-1], `\'`, `'`)
	}
	return prompt
}

func commandIdempotencyKey(comment TriggerComment, verb CommandVerb, sessionID, exactProcessID, bodyHash string) string {
	updatedAt := ""
	if !comment.UpdatedAt.IsZero() {
		updatedAt = comment.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	parts := []string{
		"runner-command-v1",
		strings.TrimSpace(comment.Repo),
		fmt.Sprintf("%d", comment.Issue),
		fmt.Sprintf("%d", comment.CommentID),
		updatedAt,
		strings.TrimSpace(comment.Commenter),
		string(verb),
		sessionID,
		exactProcessID,
		bodyHash,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "runner-command-v1:" + hex.EncodeToString(sum[:])
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
