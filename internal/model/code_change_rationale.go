package model

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const codeChangeRationaleVersion = 1

var codeChangeRationaleMarkerRe = regexp.MustCompile(`(?s)<!--\s*issue-spec:code-change-rationale\b([^>]*)-->`)

// CodeChangeRationaleMarker is the provider-neutral, append-only rationale
// identity written to the Implement Issue for a self-hosted code change. The
// payload is deliberately independent from provider evidence writer identity:
// Agent identifies the real workflow worker while the final gate separately
// requires trusted native-ledger evidence for the same PROCESS and SPEC.
type CodeChangeRationaleMarker struct {
	Process            string `json:"process"`
	Spec               string `json:"spec"`
	SpecURL            string `json:"spec_url"`
	ProviderKey        string `json:"provider_key"`
	ExternalRepository string `json:"external_repository"`
	ChangeID           string `json:"change_id"`
	ReferenceVersion   int64  `json:"reference_version"`
	SubjectRevision    string `json:"subject_revision"`
	Agent              string `json:"agent"`
	AgentSessionID     string `json:"agent_session_id"`
	AgentSessionSource string `json:"agent_session_source"`
}

// RenderCodeChangeRationaleBody renders a deterministic base64url-encoded JSON
// marker plus human-readable identity. JSON uses a struct so field ordering is
// stable and exact retries produce the same marker bytes.
func RenderCodeChangeRationaleBody(marker CodeChangeRationaleMarker, rationale string) (string, error) {
	marker = normalizeCodeChangeRationaleMarker(marker)
	if err := validateCodeChangeRationaleMarker(marker); err != nil {
		return "", err
	}
	rationale = strings.TrimSpace(rationale)
	if rationale == "" {
		return "", errors.New("rationale body is required")
	}
	payload, err := json.Marshal(marker)
	if err != nil {
		return "", fmt.Errorf("encode code-change rationale marker: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf(`<!-- issue-spec:code-change-rationale payload=%s version=%d -->
Agent: %s
Agent Session ID: %s
Agent Session Source: %s
Subject Revision: %s
Process: %s
Spec: %s
Spec Comment: %s
Provider: %s
External Repository: %s
Change: %s
Reference Version: %d

### Rationale

%s
`, encoded, codeChangeRationaleVersion, marker.Agent, marker.AgentSessionID, marker.AgentSessionSource,
		marker.SubjectRevision, marker.Process, marker.Spec, marker.SpecURL, marker.ProviderKey,
		marker.ExternalRepository, marker.ChangeID, marker.ReferenceVersion, rationale), nil
}

// FindCodeChangeRationaleMarker parses exactly one strict marker. A malformed or
// duplicate marker is reported as present plus an error so callers fail closed
// instead of silently treating it as ordinary prose.
func FindCodeChangeRationaleMarker(body string) (CodeChangeRationaleMarker, bool, error) {
	matches := codeChangeRationaleMarkerRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return CodeChangeRationaleMarker{}, false, nil
	}
	if len(matches) != 1 {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale comment must contain exactly one marker")
	}
	attrs, err := strictCodeChangeRationaleAttrs(matches[0][1])
	if err != nil {
		return CodeChangeRationaleMarker{}, true, err
	}
	if attrs["version"] != strconv.Itoa(codeChangeRationaleVersion) {
		return CodeChangeRationaleMarker{}, true, fmt.Errorf("unsupported code-change rationale marker version %q", attrs["version"])
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(attrs["payload"])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != attrs["payload"] {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale marker payload is not canonical base64url")
	}
	var marker CodeChangeRationaleMarker
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return CodeChangeRationaleMarker{}, true, fmt.Errorf("invalid code-change rationale marker payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CodeChangeRationaleMarker{}, true, errors.New("invalid trailing code-change rationale marker payload")
	}
	canonical, err := json.Marshal(marker)
	if err != nil || !bytes.Equal(payload, canonical) {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale marker payload is not canonical JSON")
	}
	normalized := normalizeCodeChangeRationaleMarker(marker)
	if normalized != marker {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale marker identity is not canonical")
	}
	marker = normalized
	if err := validateCodeChangeRationaleMarker(marker); err != nil {
		return CodeChangeRationaleMarker{}, true, err
	}
	metadata := visibleMetadata(body)
	if metadata["Agent"] != marker.Agent || metadata["Agent Session ID"] != marker.AgentSessionID ||
		metadata["Agent Session Source"] != marker.AgentSessionSource || metadata["Subject Revision"] != marker.SubjectRevision {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale visible agent, session, or revision does not match marker payload")
	}
	return marker, true, nil
}

func IsLikelyCodeChangeRationale(body string) bool {
	return codeChangeRationaleMarkerRe.MatchString(body)
}

func strictCodeChangeRationaleAttrs(raw string) (map[string]string, error) {
	attrs := map[string]string{}
	for _, field := range strings.Fields(raw) {
		key, value, ok := strings.Cut(field, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || (key != "payload" && key != "version") || value == "" {
			return nil, errors.New("code-change rationale marker requires only payload and version attributes")
		}
		if _, duplicate := attrs[key]; duplicate {
			return nil, fmt.Errorf("duplicate code-change rationale marker attribute %q", key)
		}
		attrs[key] = value
	}
	if len(attrs) != 2 || attrs["payload"] == "" || attrs["version"] == "" {
		return nil, errors.New("code-change rationale marker requires payload and version attributes")
	}
	return attrs, nil
}

func normalizeCodeChangeRationaleMarker(marker CodeChangeRationaleMarker) CodeChangeRationaleMarker {
	marker.Process = strings.TrimSpace(marker.Process)
	marker.Spec = strings.TrimSpace(marker.Spec)
	marker.SpecURL = strings.TrimSpace(marker.SpecURL)
	marker.ProviderKey = strings.TrimSpace(marker.ProviderKey)
	marker.ExternalRepository = strings.TrimSpace(marker.ExternalRepository)
	marker.ChangeID = strings.TrimSpace(marker.ChangeID)
	marker.SubjectRevision = strings.TrimSpace(marker.SubjectRevision)
	marker.Agent = strings.TrimSpace(marker.Agent)
	marker.AgentSessionID = strings.TrimSpace(marker.AgentSessionID)
	marker.AgentSessionSource = strings.TrimSpace(marker.AgentSessionSource)
	return marker
}

func validateCodeChangeRationaleMarker(marker CodeChangeRationaleMarker) error {
	if !regexp.MustCompile(`^PROCESS-[0-9]{3,}$`).MatchString(marker.Process) ||
		!regexp.MustCompile(`^SPEC-[0-9]{3,}$`).MatchString(marker.Spec) {
		return errors.New("code-change rationale requires canonical PROCESS and SPEC ids")
	}
	parsed, err := url.Parse(marker.SpecURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return errors.New("code-change rationale requires a safe absolute SPEC URL")
	}
	if marker.ProviderKey == "" || marker.ExternalRepository == "" || marker.ChangeID == "" ||
		marker.SubjectRevision == "" || marker.ReferenceVersion <= 0 {
		return errors.New("code-change rationale code-change identity is incomplete")
	}
	if len(marker.ProviderKey) > 128 || len(marker.ExternalRepository) > 512 || len(marker.ChangeID) > 256 ||
		len(marker.SubjectRevision) > 512 || len(marker.SpecURL) > 4096 || len(marker.Agent) > 256 ||
		len(marker.AgentSessionID) > 512 || strings.ContainsAny(marker.ProviderKey, " \t\r\n") ||
		strings.ContainsAny(marker.ExternalRepository, "\r\n") || strings.ContainsAny(marker.ChangeID, "\r\n") ||
		strings.ContainsAny(marker.SubjectRevision, " \t\r\n") || strings.ContainsAny(marker.SpecURL, "\r\n") ||
		strings.ContainsAny(marker.Agent, "\r\n") || strings.ContainsAny(marker.AgentSessionID, " \t\r\n") {
		return errors.New("code-change rationale code-change identity is invalid")
	}
	if marker.Agent == "" || marker.AgentSessionID == "" ||
		(marker.AgentSessionSource != "CODEX_THREAD_ID" && marker.AgentSessionSource != "agent-session-parameter") {
		return errors.New("code-change rationale requires agent and trusted agent session metadata")
	}
	return nil
}
