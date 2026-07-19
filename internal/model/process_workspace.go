package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const (
	processWorkspaceHeading              = "### Workspace"
	acceptedImplementationReceiptHeading = "### Accepted Implementation Receipt"
)

const (
	acceptedImplementationReceiptStart = "<!-- issue-spec:accepted-implementation-receipt version=1 -->"
	acceptedImplementationReceiptEnd   = "<!-- /issue-spec:accepted-implementation-receipt -->"
)

// ProcessWorkspace is the portable PROCESS projection shared with the local
// workspace lease store. PortableLease intentionally excludes machine-local
// paths, host identity, PIDs, lock tokens, and credentials.
type ProcessWorkspace = processworkspace.PortableLease

// ProcessWorkspaceResult preserves compatibility with historical PROCESS
// comments. Explicit is false when the section is absent; that remains
// readable and produces a migration warning rather than a canonical error.
type ProcessWorkspaceResult struct {
	Workspace   *ProcessWorkspace     `json:"workspace,omitempty"`
	Explicit    bool                  `json:"explicit"`
	Diagnostics []CanonicalDiagnostic `json:"diagnostics,omitempty"`
}

func (r ProcessWorkspaceResult) Blocking() bool {
	for _, diagnostic := range r.Diagnostics {
		if diagnostic.Severity == "error" {
			return true
		}
	}
	return false
}

// ParseProcessWorkspace reads the canonical fenced JSON projection from a
// PROCESS body. Unknown JSON fields are rejected so a local-only path or lease
// credential cannot be smuggled into the durable remote section.
func ParseProcessWorkspace(id, url, body string) ProcessWorkspaceResult {
	result := ProcessWorkspaceResult{}
	diagnostic := func(severity, element, message string) CanonicalDiagnostic {
		return CanonicalDiagnostic{Severity: severity, Type: "PROCESS", ID: id, URL: url, Element: element, Message: message}
	}
	authority, found, authorityErr := ObserveAcceptedReceiptAuthority(body, assignment.RoleImplementation)
	if authorityErr != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-receipt-invalid", authorityErr.Error())}
		return result
	}
	sections := markdownSectionContents(LogicalBody(body), processWorkspaceHeading)
	result.Explicit = len(sections) > 0
	if len(sections) == 0 {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("warning", "workspace-missing",
			"legacy or not-yet-managed PROCESS is missing `### Workspace`; managed execution must prepare portable metadata before dispatch")}
		return result
	}
	if len(sections) != 1 {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-duplicate",
			"PROCESS has multiple `### Workspace` sections")}
		return result
	}
	payload, err := fencedWorkspaceJSON(sections[0])
	if err != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-invalid", err.Error())}
		return result
	}
	if err := rejectDuplicateWorkspaceJSONKeys(payload); err != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-invalid", err.Error())}
		return result
	}
	if workspaceJSONContainsLocalFields(payload) {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-local-field",
			"`### Workspace` contains machine-local path, host, lock, or credential fields")}
		return result
	}
	var workspace ProcessWorkspace
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&workspace); err != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-invalid", fmt.Sprintf("invalid Workspace JSON: %v", err))}
		return result
	}
	if err := requireJSONEOF(decoder); err != nil {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-invalid", err.Error())}
		return result
	}
	if err := validatePortableProcessWorkspace(workspace); err != nil {
		element := "workspace-invalid"
		if strings.Contains(err.Error(), "schema version") {
			element = "workspace-schema-unsupported"
		}
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", element, fmt.Sprintf("invalid Workspace metadata: %v", err))}
		return result
	}
	hasPortableAuthority := workspace.AcceptedReceiptID != "" || workspace.AcceptedReceiptDigest != "" || workspace.AcceptedReceiptGeneration != 0
	if hasPortableAuthority {
		if !found {
			result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-receipt-missing",
				"portable accepted receipt authority requires one compact implementation receipt marker")}
			return result
		}
		if authority.ReceiptID != workspace.AcceptedReceiptID || authority.Digest != workspace.AcceptedReceiptDigest ||
			authority.Generation != workspace.AcceptedReceiptGeneration {
			result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-receipt-mismatch",
				"compact implementation receipt marker differs from portable Workspace authority")}
			return result
		}
	} else if found {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-receipt-orphan",
			"compact implementation receipt marker lacks portable Workspace authority")}
		return result
	}
	if id != "" && workspace.ProcessID != id {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-process-mismatch",
			fmt.Sprintf("Workspace process_id %q does not match PROCESS id %q", workspace.ProcessID, id))}
		return result
	}
	class := ParseProcessExecutionClass(id, url, body)
	if !class.Blocking() && class.Class != "" && string(workspace.ExecutionClass) != string(class.Class) {
		result.Diagnostics = []CanonicalDiagnostic{diagnostic("error", "workspace-class-mismatch",
			fmt.Sprintf("Workspace execution_class %q does not match `### Execution Class` %q", workspace.ExecutionClass, class.Class))}
		return result
	}
	result.Workspace = &workspace
	return result
}

func rejectDuplicateWorkspaceJSONKeys(payload string) error {
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	if err := consumeWorkspaceJSONValue(decoder, "$"); err != nil {
		return fmt.Errorf("invalid Workspace JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func consumeWorkspaceJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		var seen []string
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			for _, previous := range seen {
				if strings.EqualFold(previous, key) {
					return fmt.Errorf("duplicate object key %q at %s", key, path)
				}
			}
			seen = append(seen, key)
			if err := consumeWorkspaceJSONValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := consumeWorkspaceJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	return nil
}

// RenderProcessWorkspaceSection validates and serializes the portable lease in
// one stable representation suitable for a durable PROCESS comment.
func RenderProcessWorkspaceSection(workspace ProcessWorkspace) (string, error) {
	if err := validatePortableProcessWorkspace(workspace); err != nil {
		return "", fmt.Errorf("invalid PROCESS Workspace metadata: %w", err)
	}
	payload, err := json.MarshalIndent(workspace, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render PROCESS Workspace metadata: %w", err)
	}
	section := processWorkspaceHeading + "\n\n```json\n" + string(payload) + "\n```"
	if workspace.AcceptedReceiptID == "" {
		return section, nil
	}
	identity, err := json.Marshal(struct {
		ReceiptID            string `json:"receipt_id"`
		ReceiptDigest        string `json:"receipt_digest"`
		AssignmentGeneration uint64 `json:"assignment_generation"`
	}{workspace.AcceptedReceiptID, workspace.AcceptedReceiptDigest, workspace.AcceptedReceiptGeneration})
	if err != nil {
		return "", fmt.Errorf("render accepted implementation receipt authority: %w", err)
	}
	return section + "\n\n" + acceptedImplementationReceiptHeading + "\n\n" + acceptedImplementationReceiptStart + "\n" +
		string(identity) + "\n" + acceptedImplementationReceiptEnd, nil
}

func validatePortableProcessWorkspace(workspace ProcessWorkspace) error {
	if err := workspace.Validate(); err != nil {
		return err
	}
	if !portableRepositoryIdentity(workspace.Repository) {
		return errors.New("repository must be a portable owner/repository or provider:owner/repository identity")
	}
	return nil
}

var portableRepositoryComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func portableRepositoryIdentity(value string) bool {
	if value == "" || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "file:") || (len(value) >= 2 && value[1] == ':' && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z'))) {
		return false
	}
	pathIdentity := value
	if separator := strings.IndexByte(value, ':'); separator >= 0 {
		provider := value[:separator]
		if !portableRepositoryComponent.MatchString(provider) {
			return false
		}
		pathIdentity = value[separator+1:]
		if strings.Contains(pathIdentity, ":") {
			return false
		}
	}
	parts := strings.Split(pathIdentity, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !portableRepositoryComponent.MatchString(part) {
			return false
		}
	}
	return true
}

func fencedWorkspaceJSON(section string) (string, error) {
	section = strings.TrimSpace(section)
	if !strings.HasPrefix(section, "```json\n") || !strings.HasSuffix(section, "\n```") {
		return "", errors.New("`### Workspace` must contain exactly one fenced `json` object")
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(section, "```json\n"), "\n```")
	if strings.TrimSpace(payload) == "" || strings.Contains(payload, "```") {
		return "", errors.New("`### Workspace` must contain exactly one fenced `json` object")
	}
	return payload, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("invalid Workspace JSON: multiple JSON values")
	}
	return fmt.Errorf("invalid Workspace JSON: %v", err)
}

type markdownSection struct {
	Start        int
	ContentStart int
	End          int
}

type markdownLine struct {
	start, next int
	heading     string
}

func markdownSectionContents(body, heading string) []string {
	ranges := markdownSectionRanges(body, heading)
	sections := make([]string, 0, len(ranges))
	for _, section := range ranges {
		sections = append(sections, strings.TrimSpace(body[section.ContentStart:section.End]))
	}
	return sections
}

// markdownSectionRanges is the single source of truth for parsing and mutating
// level-two/three sections. Headings in fenced or four-space/tab-indented code
// are content, never structural section boundaries.
func markdownSectionRanges(body, heading string) []markdownSection {
	lines := structuralMarkdownLines(body)
	var sections []markdownSection
	for index, line := range lines {
		if line.heading != heading {
			continue
		}
		end := len(body)
		for _, candidate := range lines[index+1:] {
			if strings.HasPrefix(candidate.heading, "## ") || strings.HasPrefix(candidate.heading, "### ") {
				end = candidate.start
				break
			}
		}
		sections = append(sections, markdownSection{Start: line.start, ContentStart: line.next, End: end})
	}
	return sections
}

func workspaceSectionBounds(body string) [][2]int {
	sections := markdownSectionRanges(body, processWorkspaceHeading)
	receipts := markdownSectionRanges(body, acceptedImplementationReceiptHeading)
	bounds := make([][2]int, 0, len(sections))
	for _, section := range sections {
		end := section.End
		for _, receipt := range receipts {
			if receipt.Start == section.End {
				end = receipt.End
				break
			}
		}
		bounds = append(bounds, [2]int{section.Start, end})
	}
	return bounds
}

func structuralMarkdownLines(body string) []markdownLine {
	var lines []markdownLine
	fence, fenceWidth := byte(0), 0
	for offset := 0; offset < len(body); {
		lineEnd := strings.IndexByte(body[offset:], '\n')
		next := len(body)
		if lineEnd < 0 {
			lineEnd = len(body)
		} else {
			lineEnd += offset
			next = lineEnd + 1
		}
		line := strings.TrimSuffix(body[offset:lineEnd], "\r")
		indent := 0
		for indent < len(line) && line[indent] == ' ' && indent < 4 {
			indent++
		}
		indented := indent == 4 || (len(line) > 0 && line[0] == '\t')
		content := line[indent:]
		if !indented {
			if fence != 0 {
				if closesMarkdownFence(content, fence, fenceWidth) {
					fence, fenceWidth = 0, 0
				}
			} else if marker, width := opensMarkdownFence(content); marker != 0 {
				fence, fenceWidth = marker, width
			} else if strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "### ") {
				lines = append(lines, markdownLine{start: offset, next: next, heading: strings.TrimSpace(content)})
			}
		}
		if next == len(body) {
			break
		}
		offset = next
	}
	return lines
}

func opensMarkdownFence(line string) (byte, int) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	marker := line[0]
	width := strings.IndexFunc(line, func(r rune) bool { return byte(r) != marker })
	if width < 0 {
		width = len(line)
	}
	if width < 3 || (marker == '`' && strings.ContainsRune(line[width:], '`')) {
		return 0, 0
	}
	return marker, width
}

func closesMarkdownFence(line string, marker byte, minimum int) bool {
	width := 0
	for width < len(line) && line[width] == marker {
		width++
	}
	return width >= minimum && strings.TrimSpace(line[width:]) == ""
}

func workspaceJSONContainsLocalFields(payload string) bool {
	var value any
	if json.Unmarshal([]byte(payload), &value) != nil {
		return false
	}
	return containsLocalWorkspaceKey(value)
}

func containsLocalWorkspaceKey(value any) bool {
	local := map[string]bool{"worktree_path": true, "integration_root": true, "git_common_dir": true, "lock_token": true, "credential": true, "hostname": true, "pid": true}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if local[key] || containsLocalWorkspaceKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsLocalWorkspaceKey(child) {
				return true
			}
		}
	}
	return false
}
