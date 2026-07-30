package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

const (
	codeChangeRationaleVersionLegacy  = 1
	codeChangeRationaleVersionCurrent = 2

	CodeChangeRationalePendingExternal     = "pending_external"
	CodeChangeRationalePublishedExternal   = "published_external"
	CodeChangeRationaleExternalUnavailable = "external_unavailable"

	codeChangeRationaleIdentityNamespace = "issue-spec-rationale-sha256:"
)

var codeChangeRationaleMarkerRe = regexp.MustCompile(`(?s)<!--\s*issue-spec:code-change-rationale\b([^>]*)-->`)

type CodeChangeRationalePublication struct {
	State       string `json:"state"`
	ExternalID  string `json:"external_id,omitempty"`
	ExternalURL string `json:"external_url,omitempty"`
}

// CodeChangeRationaleMarker is the provider-neutral rationale identity written
// to the Implement Issue for a self-hosted code change. Version 1 remains
// readable as a legacy issue-only carrier. Version 2 binds one stable logical
// identity to an explicit publication state and optional external receipt.
type CodeChangeRationaleMarker struct {
	Process            string                          `json:"process"`
	Spec               string                          `json:"spec"`
	SpecURL            string                          `json:"spec_url"`
	ProviderKey        string                          `json:"provider_key"`
	ExternalRepository string                          `json:"external_repository"`
	ChangeID           string                          `json:"change_id"`
	ReferenceVersion   int64                           `json:"reference_version"`
	SubjectRevision    string                          `json:"subject_revision"`
	Agent              string                          `json:"agent"`
	AgentSessionID     string                          `json:"agent_session_id,omitempty"`
	AgentSessionSource string                          `json:"agent_session_source,omitempty"`
	RationaleID        string                          `json:"rationale_id,omitempty"`
	Publication        *CodeChangeRationalePublication `json:"publication,omitempty"`
}

// RenderCodeChangeRationaleBody renders a deterministic base64url-encoded JSON
// marker plus human-readable identity. A marker without version-2 fields is
// rendered byte-for-byte in the legacy version-1 shape. New callers prepare a
// version-2 marker with PrepareCodeChangeRationaleMarker.
func RenderCodeChangeRationaleBody(marker CodeChangeRationaleMarker, rationale string) (string, error) {
	marker = normalizeCodeChangeRationaleMarker(marker)
	version := codeChangeRationaleMarkerVersion(marker)
	rationale = normalizeCodeChangeRationaleText(rationale)
	if rationale == "" {
		return "", errors.New("rationale body is required")
	}
	if err := validateCodeChangeRationaleMarker(marker, version, rationale); err != nil {
		return "", err
	}
	payload, err := json.Marshal(marker)
	if err != nil {
		return "", fmt.Errorf("encode code-change rationale marker: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	var legacySessionLines strings.Builder
	if marker.AgentSessionID != "" {
		fmt.Fprintf(&legacySessionLines, "Agent Session ID: %s\n", marker.AgentSessionID)
	}
	if marker.AgentSessionSource != "" {
		fmt.Fprintf(&legacySessionLines, "Agent Session Source: %s\n", marker.AgentSessionSource)
	}
	var publicationLines strings.Builder
	if version == codeChangeRationaleVersionCurrent {
		fmt.Fprintf(&publicationLines, "Rationale ID: %s\nPublication State: %s\n", marker.RationaleID, marker.Publication.State)
		if marker.Publication.State == CodeChangeRationalePublishedExternal {
			fmt.Fprintf(&publicationLines, "External Comment ID: %s\nExternal Comment URL: %s\n",
				marker.Publication.ExternalID, marker.Publication.ExternalURL)
		}
	}
	return fmt.Sprintf(`<!-- issue-spec:code-change-rationale payload=%s version=%d -->
Agent: %s
%sSubject Revision: %s
Process: %s
Spec: %s
Spec Comment: %s
Provider: %s
External Repository: %s
Change: %s
Reference Version: %d
%s
### Rationale

%s
`, encoded, version, marker.Agent, legacySessionLines.String(), marker.SubjectRevision,
		marker.Process, marker.Spec, marker.SpecURL, marker.ProviderKey,
		marker.ExternalRepository, marker.ChangeID, marker.ReferenceVersion, publicationLines.String(), rationale), nil
}

// PrepareCodeChangeRationaleMarker returns a canonical version-2 marker for one
// publication state. The rationale identity excludes publication and transport
// receipts, so pending and completed renderings retain the same logical ID.
func PrepareCodeChangeRationaleMarker(marker CodeChangeRationaleMarker, rationale, state, externalID, externalURL string) (CodeChangeRationaleMarker, error) {
	marker = normalizeCodeChangeRationaleMarker(marker)
	marker.AgentSessionID, marker.AgentSessionSource = "", ""
	marker.RationaleID = ""
	marker.Publication = nil
	rationale = normalizeCodeChangeRationaleText(rationale)
	if rationale == "" {
		return CodeChangeRationaleMarker{}, errors.New("rationale body is required")
	}
	if err := validateCodeChangeRationaleIdentity(marker); err != nil {
		return CodeChangeRationaleMarker{}, err
	}
	rationaleID, err := ComputeCodeChangeRationaleID(marker, rationale)
	if err != nil {
		return CodeChangeRationaleMarker{}, err
	}
	marker.RationaleID = rationaleID
	marker.Publication = &CodeChangeRationalePublication{
		State: strings.TrimSpace(state), ExternalID: strings.TrimSpace(externalID), ExternalURL: strings.TrimSpace(externalURL),
	}
	if err := validateCodeChangeRationaleMarker(marker, codeChangeRationaleVersionCurrent, rationale); err != nil {
		return CodeChangeRationaleMarker{}, err
	}
	return marker, nil
}

// ComputeCodeChangeRationaleID hashes only canonical logical inputs. Issue
// comment IDs, publication state, provider receipts, and runtime sessions are
// deliberately excluded.
func ComputeCodeChangeRationaleID(marker CodeChangeRationaleMarker, rationale string) (string, error) {
	marker = normalizeCodeChangeRationaleMarker(marker)
	marker.AgentSessionID, marker.AgentSessionSource = "", ""
	marker.RationaleID, marker.Publication = "", nil
	if err := validateCodeChangeRationaleIdentity(marker); err != nil {
		return "", err
	}
	rationale = normalizeCodeChangeRationaleText(rationale)
	if rationale == "" {
		return "", errors.New("rationale body is required")
	}
	canonical := struct {
		ProviderKey             string `json:"provider_key"`
		ExternalRepository      string `json:"external_repository"`
		ChangeID                string `json:"change_id"`
		ReferenceVersion        int64  `json:"reference_version"`
		SubjectRevision         string `json:"subject_revision"`
		Process                 string `json:"process"`
		Spec                    string `json:"spec"`
		SpecURL                 string `json:"spec_url"`
		Agent                   string `json:"agent"`
		NormalizedRationaleBody string `json:"normalized_rationale_body"`
	}{
		ProviderKey: marker.ProviderKey, ExternalRepository: marker.ExternalRepository, ChangeID: marker.ChangeID,
		ReferenceVersion: marker.ReferenceVersion, SubjectRevision: marker.SubjectRevision, Process: marker.Process,
		Spec: marker.Spec, SpecURL: marker.SpecURL, Agent: marker.Agent, NormalizedRationaleBody: rationale,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode code-change rationale identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return codeChangeRationaleIdentityNamespace + hex.EncodeToString(digest[:]), nil
}

// RenderCodeChangeRationaleExternalProjection produces the byte-stable prose
// sent through change.comment. It contains no mutable carrier state or receipt.
func RenderCodeChangeRationaleExternalProjection(marker CodeChangeRationaleMarker, rationale string) (string, error) {
	marker = normalizeCodeChangeRationaleMarker(marker)
	rationale = normalizeCodeChangeRationaleText(rationale)
	if marker.RationaleID == "" || rationale == "" {
		return "", errors.New("version-2 rationale identity and body are required")
	}
	expected, err := ComputeCodeChangeRationaleID(marker, rationale)
	if err != nil {
		return "", err
	}
	if marker.RationaleID != expected {
		return "", errors.New("code-change rationale identity does not match canonical inputs")
	}
	return fmt.Sprintf(`### Implementation Rationale

Agent: %s
Subject Revision: %s
Process: %s
Spec: %s
Spec Comment: %s
Rationale ID: %s

### Rationale

%s
`, marker.Agent, marker.SubjectRevision, marker.Process, marker.Spec, marker.SpecURL, marker.RationaleID, rationale), nil
}

func CodeChangeRationaleVersion(marker CodeChangeRationaleMarker) int {
	return codeChangeRationaleMarkerVersion(marker)
}

func CodeChangeRationaleGateEligible(marker CodeChangeRationaleMarker) bool {
	switch codeChangeRationaleMarkerVersion(marker) {
	case codeChangeRationaleVersionLegacy:
		return true
	case codeChangeRationaleVersionCurrent:
		return marker.Publication != nil &&
			(marker.Publication.State == CodeChangeRationalePublishedExternal ||
				marker.Publication.State == CodeChangeRationaleExternalUnavailable)
	default:
		return false
	}
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
	version, err := strconv.Atoi(attrs["version"])
	if err != nil || (version != codeChangeRationaleVersionLegacy && version != codeChangeRationaleVersionCurrent) {
		return CodeChangeRationaleMarker{}, true, fmt.Errorf("unsupported code-change rationale marker version %q", attrs["version"])
	}
	if attrs["version"] != strconv.Itoa(version) {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale marker version is not canonical")
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
	if !reflect.DeepEqual(normalized, marker) {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale marker identity is not canonical")
	}
	marker = normalized
	if codeChangeRationaleMarkerVersion(marker) != version {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale marker version does not match payload")
	}
	rationale, err := codeChangeRationaleText(body)
	if err != nil {
		return CodeChangeRationaleMarker{}, true, err
	}
	if err := validateCodeChangeRationaleMarker(marker, version, rationale); err != nil {
		return CodeChangeRationaleMarker{}, true, err
	}
	metadata := visibleMetadata(body)
	expectedMetadata := map[string]string{
		"Agent": marker.Agent, "Subject Revision": marker.SubjectRevision, "Process": marker.Process,
		"Spec": marker.Spec, "Spec Comment": marker.SpecURL, "Provider": marker.ProviderKey,
		"External Repository": marker.ExternalRepository, "Change": marker.ChangeID,
		"Reference Version": strconv.FormatInt(marker.ReferenceVersion, 10),
	}
	if version == codeChangeRationaleVersionCurrent {
		expectedMetadata["Rationale ID"] = marker.RationaleID
		expectedMetadata["Publication State"] = marker.Publication.State
		if marker.Publication.State == CodeChangeRationalePublishedExternal {
			expectedMetadata["External Comment ID"] = marker.Publication.ExternalID
			expectedMetadata["External Comment URL"] = marker.Publication.ExternalURL
		}
	}
	for key, value := range expectedMetadata {
		if metadata[key] != value {
			return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale visible metadata does not match marker payload")
		}
	}
	if version == codeChangeRationaleVersionCurrent &&
		marker.Publication.State != CodeChangeRationalePublishedExternal &&
		(metadata["External Comment ID"] != "" || metadata["External Comment URL"] != "") {
		return CodeChangeRationaleMarker{}, true, errors.New("code-change rationale visible metadata does not match marker payload")
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
	marker.RationaleID = strings.TrimSpace(marker.RationaleID)
	if marker.Publication != nil {
		publication := *marker.Publication
		publication.State = strings.TrimSpace(publication.State)
		publication.ExternalID = strings.TrimSpace(publication.ExternalID)
		publication.ExternalURL = strings.TrimSpace(publication.ExternalURL)
		marker.Publication = &publication
	}
	return marker
}

func validateCodeChangeRationaleMarker(marker CodeChangeRationaleMarker, version int, rationale string) error {
	if err := validateCodeChangeRationaleIdentity(marker); err != nil {
		return err
	}
	switch version {
	case codeChangeRationaleVersionLegacy:
		if marker.RationaleID != "" || marker.Publication != nil {
			return errors.New("legacy code-change rationale contains version-2 publication fields")
		}
	case codeChangeRationaleVersionCurrent:
		if marker.AgentSessionID != "" || marker.AgentSessionSource != "" {
			return errors.New("version-2 code-change rationale must not contain runtime session identity")
		}
		if marker.Publication == nil || marker.RationaleID == "" {
			return errors.New("version-2 code-change rationale publication identity is incomplete")
		}
		expected, err := ComputeCodeChangeRationaleID(marker, rationale)
		if err != nil || expected != marker.RationaleID {
			return errors.New("version-2 code-change rationale identity does not match marker and body")
		}
		switch marker.Publication.State {
		case CodeChangeRationalePendingExternal, CodeChangeRationaleExternalUnavailable:
			if marker.Publication.ExternalID != "" || marker.Publication.ExternalURL != "" {
				return errors.New("non-published code-change rationale must not contain an external receipt")
			}
		case CodeChangeRationalePublishedExternal:
			if marker.Publication.ExternalID == "" || len(marker.Publication.ExternalID) > 512 ||
				strings.ContainsAny(marker.Publication.ExternalID, "\r\n") ||
				!safeCodeChangeRationaleURL(marker.Publication.ExternalURL) {
				return errors.New("published code-change rationale requires a valid external receipt")
			}
		default:
			return fmt.Errorf("unsupported code-change rationale publication state %q", marker.Publication.State)
		}
	default:
		return fmt.Errorf("unsupported code-change rationale marker version %d", version)
	}
	return nil
}

func validateCodeChangeRationaleIdentity(marker CodeChangeRationaleMarker) error {
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
		strings.ContainsAny(marker.ProviderKey, " \t\r\n") ||
		strings.ContainsAny(marker.ExternalRepository, "\r\n") || strings.ContainsAny(marker.ChangeID, "\r\n") ||
		strings.ContainsAny(marker.SubjectRevision, " \t\r\n") || strings.ContainsAny(marker.SpecURL, "\r\n") ||
		strings.ContainsAny(marker.Agent, "\r\n") {
		return errors.New("code-change rationale code-change identity is invalid")
	}
	if marker.Agent == "" {
		return errors.New("code-change rationale requires an agent")
	}
	return nil
}

func codeChangeRationaleMarkerVersion(marker CodeChangeRationaleMarker) int {
	if marker.RationaleID != "" || marker.Publication != nil {
		return codeChangeRationaleVersionCurrent
	}
	return codeChangeRationaleVersionLegacy
}

func normalizeCodeChangeRationaleText(rationale string) string {
	return strings.TrimSpace(rationale)
}

func codeChangeRationaleText(body string) (string, error) {
	const delimiter = "\n### Rationale\n\n"
	index := strings.Index(body, delimiter)
	if index < 0 {
		return "", errors.New("code-change rationale body is missing rationale prose")
	}
	rationale := normalizeCodeChangeRationaleText(body[index+len(delimiter):])
	if rationale == "" {
		return "", errors.New("rationale body is required")
	}
	return rationale, nil
}

func safeCodeChangeRationaleURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "?#\x00\r\n\t\\") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.Opaque == "" && parsed.RawQuery == "" && !parsed.ForceQuery &&
		parsed.Fragment == "" && parsed.RawFragment == "" && parsed.Host == strings.ToLower(parsed.Host) &&
		parsed.Port() != "443" && parsed.String() == raw
}
