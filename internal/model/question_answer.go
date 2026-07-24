package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ChoiceModelVersion = 1
	AnswerVersion      = 1

	maxChoiceOptions     = 20
	maxChoiceLabelRunes  = 200
	maxChoiceDetailRunes = 1000
	maxQuestionTextRunes = 16 * 1024
	maxDefaultAssumption = 4 * 1024
	maxCustomAnswerRunes = 4 * 1024
	choiceModelSection   = "## Choice Model"
	answerPayloadSection = "## Answer"
)

var choiceOptionIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type ChoiceMode string

const (
	ChoiceModeSingle   ChoiceMode = "single"
	ChoiceModeMultiple ChoiceMode = "multiple"
)

// ChoiceOption is one stable, bounded option in a QUESTION choice model.
type ChoiceOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Tradeoff    string `json:"tradeoff,omitempty"`
}

// ChoiceModel is the complete versioned decision surface carried by a
// choice-enabled QUESTION. A QUESTION without this section remains a legacy
// QUESTION and keeps the existing status/resolution behavior.
type ChoiceModel struct {
	Version     int            `json:"version"`
	Mode        ChoiceMode     `json:"mode"`
	Options     []ChoiceOption `json:"options"`
	AllowCustom bool           `json:"allow_custom"`
}

// QuestionSnapshot is immutable historical context copied into every ANSWER.
// It deliberately contains all decision inputs rather than a revision pointer.
type QuestionSnapshot struct {
	ID                string      `json:"id"`
	Question          string      `json:"question"`
	Blocking          bool        `json:"blocking"`
	DefaultAssumption string      `json:"default_assumption"`
	IssueURL          string      `json:"issue_url"`
	SourceURL         string      `json:"source_url"`
	ChoiceModel       ChoiceModel `json:"choice_model"`
}

// AnswerOption snapshots the display label associated with one selected stable
// option ID. Labels are data only and never reparsed as Markdown authority.
type AnswerOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// AnswerSelection is either a non-empty option selection or exclusive custom
// text. Custom text stays JSON encoded and is never interpolated into a marker.
type AnswerSelection struct {
	Options []AnswerOption `json:"options,omitempty"`
	Custom  string         `json:"custom,omitempty"`
}

type AnswerPayload struct {
	Version   int              `json:"version"`
	Question  QuestionSnapshot `json:"question"`
	Selection AnswerSelection  `json:"selection"`
}

// AnswerObservation attaches provider-owned ordering and edit evidence to an
// ANSWER representation. ProviderID is the stable comment identity (decimal on
// GitHub and an opaque stable ID on other providers).
type AnswerObservation struct {
	ProviderID string    `json:"provider_id"`
	Actor      string    `json:"actor"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// RepresentationVersion is optional provider-owned edit evidence. Providers
	// that expose a monotonic representation version set 1 for an unedited
	// comment; providers without it use exact created_at/updated_at equality.
	RepresentationVersion int64  `json:"representation_version,omitempty"`
	Body                  string `json:"body"`
	URL                   string `json:"url,omitempty"`
}

type AnswerDiagnostic struct {
	ProviderID string `json:"provider_id,omitempty"`
	AnswerID   string `json:"answer_id,omitempty"`
	Message    string `json:"message"`
}

type ResolvedAnswer struct {
	ID         string        `json:"id"`
	ProviderID string        `json:"provider_id"`
	Actor      string        `json:"actor"`
	CreatedAt  time.Time     `json:"created_at"`
	URL        string        `json:"url,omitempty"`
	Payload    AnswerPayload `json:"payload"`
	BodyDigest string        `json:"body_digest"`
}

// AnswerResolution is the one deterministic authority consumed by gates,
// status, final verification, change cards, and later Agent context selection.
type AnswerResolution struct {
	Effective   map[string]ResolvedAnswer `json:"effective"`
	Diagnostics []AnswerDiagnostic        `json:"diagnostics,omitempty"`
}

func (m ChoiceModel) Validate() error {
	if m.Version != ChoiceModelVersion {
		return fmt.Errorf("choice model version must be %d", ChoiceModelVersion)
	}
	if m.Mode != ChoiceModeSingle && m.Mode != ChoiceModeMultiple {
		return errors.New("choice model mode must be single or multiple")
	}
	if len(m.Options) == 0 || len(m.Options) > maxChoiceOptions {
		return fmt.Errorf("choice model must contain 1 to %d options", maxChoiceOptions)
	}
	seen := map[string]bool{}
	for i, option := range m.Options {
		if !choiceOptionIDRE.MatchString(option.ID) {
			return fmt.Errorf("options[%d].id %q is invalid", i, option.ID)
		}
		if seen[option.ID] {
			return fmt.Errorf("duplicate option id %q", option.ID)
		}
		seen[option.ID] = true
		if strings.TrimSpace(option.Label) == "" || utf8.RuneCountInString(option.Label) > maxChoiceLabelRunes {
			return fmt.Errorf("options[%d].label must contain 1 to %d Unicode scalars", i, maxChoiceLabelRunes)
		}
		if utf8.RuneCountInString(option.Description) > maxChoiceDetailRunes {
			return fmt.Errorf("options[%d].description exceeds %d Unicode scalars", i, maxChoiceDetailRunes)
		}
		if utf8.RuneCountInString(option.Tradeoff) > maxChoiceDetailRunes {
			return fmt.Errorf("options[%d].tradeoff exceeds %d Unicode scalars", i, maxChoiceDetailRunes)
		}
	}
	return nil
}

func (s QuestionSnapshot) Validate() error {
	if err := ValidateTypedIdentity("QUESTION", s.ID); err != nil {
		return err
	}
	if strings.TrimSpace(s.Question) == "" || utf8.RuneCountInString(s.Question) > maxQuestionTextRunes {
		return fmt.Errorf("question text must contain 1 to %d Unicode scalars", maxQuestionTextRunes)
	}
	if utf8.RuneCountInString(s.DefaultAssumption) > maxDefaultAssumption {
		return fmt.Errorf("default assumption exceeds %d Unicode scalars", maxDefaultAssumption)
	}
	if err := validateSourceURL(s.SourceURL); err != nil {
		return err
	}
	if err := validateSourceURL(s.IssueURL); err != nil {
		return fmt.Errorf("owning issue: %w", err)
	}
	return s.ChoiceModel.Validate()
}

func (p AnswerPayload) Validate() error {
	if p.Version != AnswerVersion {
		return fmt.Errorf("answer version must be %d", AnswerVersion)
	}
	if err := p.Question.Validate(); err != nil {
		return fmt.Errorf("question snapshot: %w", err)
	}
	if len(p.Selection.Options) > 0 && strings.TrimSpace(p.Selection.Custom) != "" {
		return errors.New("custom text is exclusive with predefined options")
	}
	if strings.TrimSpace(p.Selection.Custom) != "" {
		if !p.Question.ChoiceModel.AllowCustom {
			return errors.New("custom text is not allowed by the question snapshot")
		}
		if utf8.RuneCountInString(p.Selection.Custom) > maxCustomAnswerRunes {
			return fmt.Errorf("custom text exceeds %d Unicode scalars", maxCustomAnswerRunes)
		}
		return nil
	}
	if len(p.Selection.Options) == 0 {
		return errors.New("answer must select options or provide custom text")
	}
	known := map[string]string{}
	for _, option := range p.Question.ChoiceModel.Options {
		known[option.ID] = option.Label
	}
	seen := map[string]bool{}
	for i, selected := range p.Selection.Options {
		label, ok := known[selected.ID]
		if !ok {
			return fmt.Errorf("selection.options[%d] references unknown option %q", i, selected.ID)
		}
		if seen[selected.ID] {
			return fmt.Errorf("selection contains duplicate option %q", selected.ID)
		}
		seen[selected.ID] = true
		if selected.Label != label {
			return fmt.Errorf("selection.options[%d] label does not match snapshot option %q", i, selected.ID)
		}
	}
	if p.Question.ChoiceModel.Mode == ChoiceModeSingle && len(p.Selection.Options) != 1 {
		return errors.New("single choice answer must select exactly one option")
	}
	return nil
}

func validateSourceURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil {
		return errors.New("question source_url must be an absolute http or https URL without userinfo")
	}
	return nil
}

// ParseChoiceModel returns found=false for legacy QUESTION comments.
func ParseChoiceModel(body string) (ChoiceModel, bool, error) {
	raw, found, err := canonicalJSONSection(LogicalBody(body), choiceModelSection)
	if err != nil || !found {
		return ChoiceModel{}, found, err
	}
	var result ChoiceModel
	if err := decodeStrictCanonicalJSON(raw, &result); err != nil {
		return ChoiceModel{}, true, fmt.Errorf("choice model: %w", err)
	}
	if err := result.Validate(); err != nil {
		return ChoiceModel{}, true, err
	}
	return result, true, nil
}

func ParseAnswerPayload(body string) (AnswerPayload, error) {
	tc := ParseTypedComment(body)
	if tc.Type != "ANSWER" || tc.Status != "done" || len(tc.Errors) > 0 {
		return AnswerPayload{}, errors.New("ANSWER requires a valid typed identity and immutable Status: done")
	}
	raw, found, err := canonicalJSONSection(LogicalBody(body), answerPayloadSection)
	if err != nil {
		return AnswerPayload{}, err
	}
	if !found {
		return AnswerPayload{}, errors.New("ANSWER is missing ## Answer canonical JSON")
	}
	var payload AnswerPayload
	if err := decodeStrictCanonicalJSON(raw, &payload); err != nil {
		return AnswerPayload{}, fmt.Errorf("answer payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return AnswerPayload{}, err
	}
	return payload, nil
}

// SnapshotQuestion reads the current QUESTION representation for trusted
// submission. The source URL comes from provider identity, never body prose.
func SnapshotQuestion(body, sourceURL string) (QuestionSnapshot, error) {
	tc := ParseTypedComment(body)
	if tc.Type != "QUESTION" || len(tc.Errors) > 0 {
		return QuestionSnapshot{}, errors.New("source is not a valid QUESTION")
	}
	choice, found, err := ParseChoiceModel(body)
	if err != nil {
		return QuestionSnapshot{}, err
	}
	if !found {
		return QuestionSnapshot{}, errors.New("legacy QUESTION has no choice model")
	}
	question, err := uniqueSectionText(LogicalBody(body), "## Question")
	if err != nil {
		return QuestionSnapshot{}, err
	}
	blockingText, err := uniqueSectionText(LogicalBody(body), "## Blocking")
	if err != nil {
		return QuestionSnapshot{}, err
	}
	var blocking bool
	if blockingText == "true" {
		blocking = true
	} else if blockingText != "false" {
		return QuestionSnapshot{}, errors.New("QUESTION Blocking must be true or false")
	}
	assumption, err := uniqueSectionText(LogicalBody(body), "## Default Assumption")
	if err != nil {
		return QuestionSnapshot{}, err
	}
	snapshot := QuestionSnapshot{
		ID: tc.ID, Question: question, Blocking: blocking,
		DefaultAssumption: assumption, IssueURL: owningIssueURL(sourceURL),
		SourceURL: strings.TrimSpace(sourceURL), ChoiceModel: choice,
	}
	if err := snapshot.Validate(); err != nil {
		return QuestionSnapshot{}, err
	}
	return snapshot, nil
}

func owningIssueURL(sourceURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil {
		return strings.TrimSpace(sourceURL)
	}
	parsed.Fragment = ""
	return parsed.String()
}

func BuildAnswerPayload(snapshot QuestionSnapshot, optionIDs []string, custom string) (AnswerPayload, error) {
	payload := AnswerPayload{Version: AnswerVersion, Question: snapshot}
	custom = strings.TrimSpace(custom)
	if custom != "" {
		payload.Selection.Custom = custom
	} else {
		byID := map[string]ChoiceOption{}
		for _, option := range snapshot.ChoiceModel.Options {
			byID[option.ID] = option
		}
		requested := map[string]bool{}
		for _, id := range optionIDs {
			id = strings.TrimSpace(id)
			if _, ok := byID[id]; !ok {
				return AnswerPayload{}, fmt.Errorf("unknown option %q", id)
			}
			if requested[id] {
				return AnswerPayload{}, fmt.Errorf("selection contains duplicate option %q", id)
			}
			requested[id] = true
		}
		for _, option := range snapshot.ChoiceModel.Options {
			if !requested[option.ID] {
				continue
			}
			payload.Selection.Options = append(payload.Selection.Options, AnswerOption{ID: option.ID, Label: option.Label})
		}
	}
	if err := payload.Validate(); err != nil {
		return AnswerPayload{}, err
	}
	return payload, nil
}

func ResolveEffectiveAnswers(observations []AnswerObservation) AnswerResolution {
	result := AnswerResolution{Effective: map[string]ResolvedAnswer{}}
	ordered := append([]AnswerObservation(nil), observations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return compareStableProviderID(ordered[i].ProviderID, ordered[j].ProviderID) < 0
	})
	seenAnswerIDs := map[string]bool{}
	for _, observation := range ordered {
		tc := ParseTypedComment(observation.Body)
		diagnostic := AnswerDiagnostic{ProviderID: observation.ProviderID, AnswerID: tc.ID}
		if strings.TrimSpace(observation.ProviderID) == "" || strings.TrimSpace(observation.Actor) == "" ||
			observation.CreatedAt.IsZero() || observation.UpdatedAt.IsZero() {
			diagnostic.Message = "ANSWER lacks complete provider actor, created_at, updated_at, or stable comment ID evidence"
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			continue
		}
		edited := observation.RepresentationVersion > 1 ||
			(observation.RepresentationVersion == 0 && !observation.CreatedAt.Equal(observation.UpdatedAt))
		if edited {
			diagnostic.Message = "edited ANSWER is not workflow authority; append a new ANSWER"
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			continue
		}
		if seenAnswerIDs[tc.ID] {
			diagnostic.Message = "duplicate ANSWER typed identity is not workflow authority"
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			continue
		}
		payload, err := ParseAnswerPayload(observation.Body)
		if err != nil {
			diagnostic.Message = err.Error()
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			continue
		}
		seenAnswerIDs[tc.ID] = true
		result.Effective[payload.Question.ID] = ResolvedAnswer{
			ID: tc.ID, ProviderID: observation.ProviderID, Actor: observation.Actor,
			CreatedAt: observation.CreatedAt, URL: observation.URL, Payload: payload,
			BodyDigest: RepresentationDigest(observation.Body),
		}
	}
	return result
}

func QuestionIsSatisfied(question TypedComment, resolution AnswerResolution) bool {
	if question.Type != "QUESTION" || question.Status != "blocked" {
		return true
	}
	if _, found, err := ParseChoiceModel(question.Body); err != nil || !found {
		// Legacy QUESTION compatibility continues to use its status.
		return false
	}
	_, ok := resolution.Effective[question.ID]
	return ok
}

func compareStableProviderID(left, right string) int {
	var leftInt, rightInt big.Int
	leftOK, rightOK := false, false
	if _, ok := leftInt.SetString(left, 10); ok {
		leftOK = true
	}
	if _, ok := rightInt.SetString(right, 10); ok {
		rightOK = true
	}
	if leftOK && rightOK {
		return leftInt.Cmp(&rightInt)
	}
	return strings.Compare(left, right)
}

// CanonicalJSON returns the compact field-ordered encoding used by typed
// QUESTION and ANSWER sections.
func CanonicalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeStrictCanonicalJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON content")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return err
	}
	if !bytes.Equal(compact.Bytes(), canonical) {
		return errors.New("JSON is not the canonical strict representation")
	}
	return nil
}

func canonicalJSONSection(body, heading string) (string, bool, error) {
	sections := markdownSectionContents(body, heading)
	if len(sections) == 0 {
		return "", false, nil
	}
	if len(sections) != 1 {
		return "", true, fmt.Errorf("%s must occur exactly once", heading)
	}
	section := strings.TrimSpace(sections[0])
	if !strings.HasPrefix(section, "```json\n") || !strings.HasSuffix(section, "\n```") {
		return "", true, fmt.Errorf("%s must contain exactly one fenced json object", heading)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(section, "```json\n"), "\n```")
	if strings.TrimSpace(raw) == "" || strings.Contains(raw, "```") {
		return "", true, fmt.Errorf("%s must contain exactly one fenced json object", heading)
	}
	return raw, true, nil
}

func uniqueSectionText(body, heading string) (string, error) {
	sections := markdownSectionContents(body, heading)
	if len(sections) != 1 {
		return "", fmt.Errorf("%s must occur exactly once", heading)
	}
	value := strings.TrimSpace(sections[0])
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", heading)
	}
	return value, nil
}
