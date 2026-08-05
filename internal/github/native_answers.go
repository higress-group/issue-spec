package github

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/higress-group/issue-spec/internal/model"
)

const (
	maxNativeAnswerOptions     = 20
	maxNativeAnswerCustomRunes = 4 * 1024
)

var nativeAnswerOptionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// NativeQuestionAnswerOperations exposes only the trusted self-hosted
// QUESTION confirmation and append-only ANSWER creation operations.
type NativeQuestionAnswerOperations interface {
	GetNativeQuestion(context.Context, string, int, string) (NativeQuestionAuthority, error)
	CreateNativeAnswer(context.Context, string, int, NativeAnswerIntent) (NativeAnswerResult, error)
}

// NativeQuestionAuthority is the server-owned current QUESTION representation
// used to validate bounded answer intent.
type NativeQuestionAuthority struct {
	Question              model.QuestionSnapshot `json:"question"`
	RepresentationVersion int64                  `json:"representation_version"`
	BodyDigest            string                 `json:"body_digest"`
	EffectiveAnswer       *NativeEffectiveAnswer `json:"effective_answer,omitempty"`
}

// NativeEffectiveAnswer is optional display context returned with current
// QUESTION authority. Stored ANSWER comments remain authoritative.
type NativeEffectiveAnswer struct {
	ID        string                `json:"id"`
	CommentID int64                 `json:"comment_id"`
	Actor     string                `json:"actor"`
	CreatedAt time.Time             `json:"created_at"`
	Selection model.AnswerSelection `json:"selection"`
	SourceURL string                `json:"source_url"`
}

// NativeAnswerIntent contains only caller selection/custom intent bound to the
// exact current QUESTION digest. The server owns ANSWER identity and rendering.
type NativeAnswerIntent struct {
	QuestionID     string   `json:"question_id"`
	QuestionDigest string   `json:"question_digest"`
	OptionIDs      []string `json:"option_ids"`
	Custom         string   `json:"custom"`
}

// NativeAnswerResult contains the canonical server-created comment and the
// QUESTION authority observed by the append transaction.
type NativeAnswerResult struct {
	Comment                       Comment                `json:"comment"`
	Question                      model.QuestionSnapshot `json:"question"`
	QuestionRepresentationVersion int64                  `json:"question_representation_version"`
	QuestionBodyDigest            string                 `json:"question_body_digest"`
}

func (c *Client) GetNativeQuestion(ctx context.Context, repo string, issue int, questionID string) (NativeQuestionAuthority, error) {
	repoPath, err := nativeAnswerRepoPath(repo)
	if err != nil {
		return NativeQuestionAuthority{}, err
	}
	questionID = strings.TrimSpace(questionID)
	if issue <= 0 {
		return NativeQuestionAuthority{}, errors.New("issue number must be positive")
	}
	if err := model.ValidateTypedIdentity("QUESTION", questionID); err != nil {
		return NativeQuestionAuthority{}, err
	}
	var result NativeQuestionAuthority
	path := fmt.Sprintf("/repos/%s/issues/%d/questions/%s", repoPath, issue, url.PathEscape(questionID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return NativeQuestionAuthority{}, err
	}
	if err := validateNativeQuestionAuthority(result, questionID); err != nil {
		return NativeQuestionAuthority{}, fmt.Errorf("invalid native QUESTION response: %w", err)
	}
	return result, nil
}

func (c *Client) CreateNativeAnswer(ctx context.Context, repo string, issue int, intent NativeAnswerIntent) (NativeAnswerResult, error) {
	repoPath, err := nativeAnswerRepoPath(repo)
	if err != nil {
		return NativeAnswerResult{}, err
	}
	if issue <= 0 {
		return NativeAnswerResult{}, errors.New("issue number must be positive")
	}
	intent.QuestionID = strings.TrimSpace(intent.QuestionID)
	if intent.OptionIDs == nil {
		intent.OptionIDs = []string{}
	}
	if err := validateNativeAnswerIntent(intent); err != nil {
		return NativeAnswerResult{}, err
	}
	var result NativeAnswerResult
	path := fmt.Sprintf("/repos/%s/issues/%d/answers", repoPath, issue)
	if err := c.doJSON(ctx, http.MethodPost, path, intent, &result); err != nil {
		return NativeAnswerResult{}, err
	}
	if err := validateNativeAnswerResult(result, intent, int64(issue)); err != nil {
		return NativeAnswerResult{}, fmt.Errorf("invalid native ANSWER response: %w", err)
	}
	return result, nil
}

func nativeAnswerRepoPath(repo string) (string, error) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", errors.New("repository owner path encoding is invalid")
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil {
		return "", errors.New("repository name path encoding is invalid")
	}
	return ParseRepo(owner + "/" + name)
}

func validateNativeQuestionAuthority(authority NativeQuestionAuthority, questionID string) error {
	if err := authority.Question.Validate(); err != nil {
		return fmt.Errorf("question snapshot: %w", err)
	}
	if authority.Question.ID != questionID {
		return errors.New("question identity does not match the requested QUESTION")
	}
	if authority.RepresentationVersion <= 0 {
		return errors.New("question representation version must be positive")
	}
	if err := validateNativeQuestionDigest(authority.BodyDigest); err != nil {
		return err
	}
	return nil
}

func validateNativeAnswerIntent(intent NativeAnswerIntent) error {
	if err := model.ValidateTypedIdentity("QUESTION", intent.QuestionID); err != nil {
		return err
	}
	if err := validateNativeQuestionDigest(intent.QuestionDigest); err != nil {
		return err
	}
	if len(intent.OptionIDs) > maxNativeAnswerOptions {
		return fmt.Errorf("answer intent exceeds %d selected options", maxNativeAnswerOptions)
	}
	seen := make(map[string]struct{}, len(intent.OptionIDs))
	for _, optionID := range intent.OptionIDs {
		if !nativeAnswerOptionIDPattern.MatchString(optionID) {
			return errors.New("answer intent contains an invalid option id")
		}
		if _, duplicate := seen[optionID]; duplicate {
			return errors.New("answer intent contains a duplicate option id")
		}
		seen[optionID] = struct{}{}
	}
	custom := strings.TrimSpace(intent.Custom)
	if custom != "" && len(intent.OptionIDs) > 0 {
		return errors.New("custom answer intent is exclusive with selected options")
	}
	if custom == "" && len(intent.OptionIDs) == 0 {
		return errors.New("answer intent must select options or provide custom text")
	}
	if utf8.RuneCountInString(custom) > maxNativeAnswerCustomRunes {
		return fmt.Errorf("custom answer intent exceeds %d Unicode scalars", maxNativeAnswerCustomRunes)
	}
	return nil
}

func validateNativeAnswerResult(result NativeAnswerResult, intent NativeAnswerIntent, issue int64) error {
	if result.Comment.ID <= 0 || strings.TrimSpace(result.Comment.Body) == "" ||
		strings.TrimSpace(result.Comment.HTMLURL) == "" || strings.TrimSpace(result.Comment.URL) == "" {
		return errors.New("canonical comment is incomplete")
	}
	authority := NativeQuestionAuthority{
		Question: result.Question, RepresentationVersion: result.QuestionRepresentationVersion,
		BodyDigest: result.QuestionBodyDigest,
	}
	if err := validateNativeQuestionAuthority(authority, strings.TrimSpace(intent.QuestionID)); err != nil {
		return err
	}
	if result.QuestionBodyDigest != intent.QuestionDigest {
		return errors.New("created ANSWER does not report the submitted QUESTION digest")
	}
	answer := model.ParseTypedComment(result.Comment.Body)
	if len(answer.Errors) > 0 || answer.Type != "ANSWER" {
		return errors.New("canonical comment is not a valid ANSWER")
	}
	if err := model.ValidateIssueScopedTypedIdentity("ANSWER", answer.ID, issue); err != nil {
		return err
	}
	return nil
}

func validateNativeQuestionDigest(digest string) error {
	if len(digest) != 64 {
		return errors.New("question body digest must be lowercase SHA-256")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || hex.EncodeToString(decoded) != digest {
		return errors.New("question body digest must be lowercase SHA-256")
	}
	return nil
}

var _ NativeQuestionAnswerOperations = (*Client)(nil)
