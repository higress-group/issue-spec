package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	supersededByStart = "<!-- issue-spec:superseded-by version=1 -->"
	supersededByEnd   = "<!-- /issue-spec:superseded-by -->"
	supersededByToken = "issue-spec:superseded-by"
)

// ErrSupersededByConflict reports an attempt to replace an already persisted
// superseded-by authority with a different target.
var ErrSupersededByConflict = errors.New("PROCESS already has a different superseded-by target")

// SupersededBy is the version-1 compact replacement authority carried by a
// historical PROCESS body. ProcessID identifies the successor and URL is its
// exact provider identity.
type SupersededBy struct {
	ProcessID string `json:"process_id"`
	URL       string `json:"url"`
}

// ParseSupersededBy parses at most one canonical version-1 marker. found is
// true whenever superseded-by marker material is present, including malformed
// material, so callers can fail closed rather than treating it as absent.
func ParseSupersededBy(body, sourceProcessID string) (value SupersededBy, found bool, err error) {
	sourceProcessID = strings.TrimSpace(sourceProcessID)
	if err := ValidateTypedIdentity("PROCESS", sourceProcessID); err != nil {
		return SupersededBy{}, false, fmt.Errorf("source PROCESS identity: %w", err)
	}
	if !strings.Contains(body, supersededByToken) {
		return SupersededBy{}, false, nil
	}
	if strings.Count(body, supersededByStart) != 1 || strings.Count(body, supersededByEnd) != 1 ||
		strings.Count(body, supersededByToken) != 2 {
		return SupersededBy{}, true, errors.New("superseded-by authority must contain exactly one version-1 marker pair")
	}
	start := strings.Index(body, supersededByStart)
	end := strings.Index(body, supersededByEnd)
	if end <= start {
		return SupersededBy{}, true, errors.New("superseded-by marker order is invalid")
	}
	rawBlock := body[start+len(supersededByStart) : end]
	if len(rawBlock) < 3 || rawBlock[0] != '\n' || rawBlock[len(rawBlock)-1] != '\n' {
		return SupersededBy{}, true, errors.New("superseded-by payload framing is invalid")
	}
	raw := []byte(rawBlock[1 : len(rawBlock)-1])
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || !bytes.Equal(compact.Bytes(), raw) {
		return SupersededBy{}, true, errors.New("superseded-by payload is not compact JSON")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return SupersededBy{}, true, fmt.Errorf("invalid superseded-by payload: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, raw) {
		return SupersededBy{}, true, errors.New("superseded-by payload is not canonical JSON")
	}
	if err := validateSupersededBy(sourceProcessID, value); err != nil {
		return SupersededBy{}, true, err
	}
	return value, true, nil
}

// StampSupersededBy appends one canonical superseded-by authority to the
// matching typed PROCESS body. Repeating the same stamp is byte-idempotent;
// trying to change its target fails with ErrSupersededByConflict.
func StampSupersededBy(body, sourceProcessID string, value SupersededBy) (string, bool, error) {
	sourceProcessID = strings.TrimSpace(sourceProcessID)
	if err := validateSupersededBy(sourceProcessID, value); err != nil {
		return "", false, err
	}
	typed := ParseTypedComment(body)
	if !HasTypedMarker(body) || !typed.HasHead || len(typed.Errors) != 0 ||
		typed.Type != "PROCESS" || typed.ID != sourceProcessID {
		return "", false, fmt.Errorf("typed comment is %s/%s, expected valid PROCESS/%s", typed.Type, typed.ID, sourceProcessID)
	}
	existing, found, err := ParseSupersededBy(body, sourceProcessID)
	if err != nil {
		return "", false, err
	}
	if found {
		if existing == value {
			return body, false, nil
		}
		return "", false, ErrSupersededByConflict
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", false, fmt.Errorf("encode superseded-by payload: %w", err)
	}
	block := supersededByStart + "\n" + string(payload) + "\n" + supersededByEnd + "\n"
	switch {
	case strings.HasSuffix(body, "\n\n"):
		return body + block, true, nil
	case strings.HasSuffix(body, "\n"):
		return body + "\n" + block, true, nil
	default:
		return body + "\n\n" + block, true, nil
	}
}

func validateSupersededBy(sourceProcessID string, value SupersededBy) error {
	if err := ValidateTypedIdentity("PROCESS", strings.TrimSpace(sourceProcessID)); err != nil {
		return fmt.Errorf("source PROCESS identity: %w", err)
	}
	if value.ProcessID != strings.TrimSpace(value.ProcessID) {
		return errors.New("superseded-by process_id is not canonical")
	}
	if err := ValidateTypedIdentity("PROCESS", value.ProcessID); err != nil {
		return fmt.Errorf("superseded-by process_id: %w", err)
	}
	if value.ProcessID == sourceProcessID {
		return errors.New("superseded-by relationship cannot target itself")
	}
	if value.URL != strings.TrimSpace(value.URL) || value.URL == "" || strings.ContainsAny(value.URL, "\r\n\t") {
		return errors.New("superseded-by url is not canonical")
	}
	parsed, err := url.Parse(value.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.String() != value.URL {
		return errors.New("superseded-by url must be an absolute canonical HTTPS provider identity")
	}
	return nil
}
