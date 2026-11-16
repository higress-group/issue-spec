package feedback

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const occurrencePrefix = "<!-- issue-spec:feedback-occurrence:v1 "
const occurrenceSuffix = " -->"

var (
	instancePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,159}$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9._-]+/[a-z0-9._-]+$`)
	eventIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,191}$`)
)

type Event struct {
	SourceInstance   string    `json:"source_instance"`
	SourceRepository string    `json:"source_repository"`
	EventID          string    `json:"event_id"`
	ObservedAt       time.Time `json:"observed_at"`
	Revision         string    `json:"revision,omitempty"`
	Summary          string    `json:"summary"`
	Evidence         []string  `json:"evidence"`
}

func ParseEvent(input string) (Event, error) {
	var event Event
	if err := decodeJSON(input, &event); err != nil {
		return Event{}, fmt.Errorf("decode feedback event: %w", err)
	}
	event.SourceInstance = strings.ToLower(strings.TrimSpace(event.SourceInstance))
	event.SourceRepository = strings.ToLower(strings.TrimSpace(event.SourceRepository))
	event.EventID = strings.TrimSpace(event.EventID)
	event.Summary = strings.TrimSpace(event.Summary)
	event.Revision = strings.TrimSpace(event.Revision)
	if !instancePattern.MatchString(event.SourceInstance) || !repositoryPattern.MatchString(event.SourceRepository) {
		return Event{}, fmt.Errorf("provide the source instance and repository in owner/name form")
	}
	if !eventIDPattern.MatchString(event.EventID) {
		return Event{}, fmt.Errorf("provide a stable event_id of up to 192 letters, digits or ._:/@- characters")
	}
	if event.ObservedAt.IsZero() || event.Summary == "" || len(event.Evidence) == 0 {
		return Event{}, fmt.Errorf("observed_at, summary and at least one evidence link are required")
	}
	for _, link := range event.Evidence {
		u, err := url.Parse(link)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return Event{}, fmt.Errorf("evidence links must be HTTP or HTTPS URLs without user information")
		}
	}
	return event, nil
}

func (e Event) Key() string {
	return e.SourceInstance + "/" + e.SourceRepository + "/" + e.EventID
}

func (e Event) commentBody() (string, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s%s%s\n\n### Workflow feedback occurrence\n\n", occurrencePrefix, data, occurrenceSuffix)
	fmt.Fprintf(&body, "%s\n\n- Observed %s\n- Source %s\n", html.EscapeString(e.Summary), e.ObservedAt.Format(time.RFC3339), e.Key())
	if e.Revision != "" {
		fmt.Fprintf(&body, "- Revision %s\n", html.EscapeString(e.Revision))
	}
	for _, link := range e.Evidence {
		fmt.Fprintf(&body, "- Evidence <%s>\n", html.EscapeString(link))
	}
	return body.String(), nil
}

// Only marker lines at the start of a provider-authored body are records.
func appendEvents(body string, yield func(Event)) {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, occurrencePrefix) || !strings.HasSuffix(line, occurrenceSuffix) {
			return
		}
		input := strings.TrimSuffix(strings.TrimPrefix(line, occurrencePrefix), occurrenceSuffix)
		if event, err := ParseEvent(input); err == nil {
			yield(event)
		}
	}
}

func decodeJSON(input string, dest any) error {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
