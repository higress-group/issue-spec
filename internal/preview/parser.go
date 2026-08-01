// Package preview recognizes versioned html-preview fenced code blocks.
//
// Parsing is deliberately byte-oriented: provider bodies are never rewritten,
// and every range is an exact half-open byte range into the original body.
// Callers that interpret issue-spec syntax should use SemanticView so preview
// source remains opaque without changing the surrounding Markdown.
package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxSourceSize = 256 * 1024
	DefaultHeight = 480
	MinHeight     = 240
	MaxHeight     = 720
	maxInfoSize   = 4096
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ByteRange is a half-open byte range into an exact provider body.
type ByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Diagnostic explains why a recognized html-preview block is inert.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Descriptor describes one recognized html-preview block. Range includes the
// fences; SourceRange contains only the exact bytes between them.
type Descriptor struct {
	ID               string       `json:"id,omitempty"`
	Version          int          `json:"version,omitempty"`
	Title            string       `json:"title,omitempty"`
	Height           int          `json:"height"`
	SourceLocator    string       `json:"source_locator,omitempty"`
	Range            ByteRange    `json:"range"`
	SourceRange      ByteRange    `json:"source_range"`
	ByteSize         int          `json:"byte_size"`
	Digest           string       `json:"digest"`
	Omitted          bool         `json:"omitted"`
	Executable       bool         `json:"executable"`
	ExpansionCommand string       `json:"expansion_command,omitempty"`
	Diagnostics      []Diagnostic `json:"diagnostics,omitempty"`
}

// Selection is the exact source selected from one executable descriptor.
type Selection struct {
	Descriptor Descriptor `json:"descriptor"`
	Source     string     `json:"source"`
}

// Result retains an unexported reference to the original body so semantic and
// exact-selection views cannot accidentally operate on a normalized copy.
type Result struct {
	Descriptors []Descriptor `json:"descriptors"`
	body        string
}

// Parse recognizes previews without attaching a provider-specific locator.
func Parse(body string) Result {
	return ParseWithSource(body, "")
}

// ParseWithSource recognizes previews and copies sourceLocator into each
// descriptor. The locator is opaque data; the parser never interprets it.
func ParseWithSource(body, sourceLocator string) Result {
	result := Result{body: body}
	lines := splitLines(body)

	for i := 0; i < len(lines); {
		opener, ok := parseOpener(body[lines[i].start:lines[i].contentEnd])
		if !ok {
			i++
			continue
		}

		closeLine := -1
		for j := i + 1; j < len(lines); j++ {
			if isCloser(body[lines[j].start:lines[j].contentEnd], opener.char, opener.length) {
				closeLine = j
				break
			}
		}

		if !opener.preview {
			if closeLine < 0 {
				break
			}
			i = closeLine + 1
			continue
		}

		wholeEnd := len(body)
		sourceEnd := len(body)
		if closeLine >= 0 {
			wholeEnd = lines[closeLine].end
			sourceEnd = lines[closeLine].start
		}
		sourceStart := lines[i].end
		source := body[sourceStart:sourceEnd]
		sum := sha256.Sum256([]byte(source))
		descriptor := Descriptor{
			Height:        DefaultHeight,
			SourceLocator: sourceLocator,
			Range:         ByteRange{Start: lines[i].start, End: wholeEnd},
			SourceRange:   ByteRange{Start: sourceStart, End: sourceEnd},
			ByteSize:      len(source),
			Digest:        hex.EncodeToString(sum[:]),
			Omitted:       true,
		}
		applyMetadata(&descriptor, opener.metadata)
		if descriptor.ID != "" && idPattern.MatchString(descriptor.ID) {
			descriptor.ExpansionCommand = "issue-spec read issue --expand-preview " + descriptor.ID
		}
		if closeLine < 0 {
			addDiagnostic(&descriptor, "unclosed_fence", "html-preview fence is not closed")
		}
		if len(source) > MaxSourceSize {
			addDiagnostic(&descriptor, "source_too_large", fmt.Sprintf("html-preview source is %d bytes; maximum is %d", len(source), MaxSourceSize))
		}
		result.Descriptors = append(result.Descriptors, descriptor)

		if closeLine < 0 {
			break
		}
		i = closeLine + 1
	}

	ids := map[string][]int{}
	for i := range result.Descriptors {
		id := result.Descriptors[i].ID
		if id != "" && idPattern.MatchString(id) {
			ids[id] = append(ids[id], i)
		}
	}
	for id, indexes := range ids {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			addDiagnostic(&result.Descriptors[index], "duplicate_id", fmt.Sprintf("html-preview id %q is duplicated", id))
		}
	}
	for i := range result.Descriptors {
		result.Descriptors[i].Executable = len(result.Descriptors[i].Diagnostics) == 0
	}
	return result
}

// SemanticView returns a byte-length-preserving view with preview source bytes
// replaced by spaces. Fence lines and all newline bytes remain exact, so
// ordinary Markdown outside previews keeps its existing parsing behavior.
func (r Result) SemanticView() string {
	if len(r.Descriptors) == 0 {
		return r.body
	}
	out := []byte(r.body)
	for _, descriptor := range r.Descriptors {
		for i := descriptor.SourceRange.Start; i < descriptor.SourceRange.End; i++ {
			if out[i] != '\r' && out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return string(out)
}

// SemanticView parses body and returns its opaque-source semantic view.
func SemanticView(body string) string {
	return Parse(body).SemanticView()
}

// Select returns the exact source for one uniquely identified executable
// preview. Unknown, duplicate, malformed, unsupported, unclosed, or
// oversized previews fail closed.
func (r Result) Select(id string) (Selection, error) {
	id = strings.TrimSpace(id)
	if !idPattern.MatchString(id) {
		return Selection{}, fmt.Errorf("invalid html-preview id %q", id)
	}
	var matches []Descriptor
	for _, descriptor := range r.Descriptors {
		if descriptor.ID == id {
			matches = append(matches, descriptor)
		}
	}
	if len(matches) == 0 {
		return Selection{}, fmt.Errorf("html-preview %q was not found", id)
	}
	if len(matches) != 1 {
		return Selection{}, fmt.Errorf("html-preview %q is ambiguous", id)
	}
	descriptor := matches[0]
	if !descriptor.Executable {
		codes := make([]string, 0, len(descriptor.Diagnostics))
		for _, diagnostic := range descriptor.Diagnostics {
			codes = append(codes, diagnostic.Code)
		}
		return Selection{}, fmt.Errorf("html-preview %q is not executable: %s", id, strings.Join(codes, ", "))
	}
	if descriptor.SourceRange.Start < 0 || descriptor.SourceRange.End < descriptor.SourceRange.Start || descriptor.SourceRange.End > len(r.body) {
		return Selection{}, errors.New("html-preview source range is invalid")
	}
	return Selection{Descriptor: descriptor, Source: r.body[descriptor.SourceRange.Start:descriptor.SourceRange.End]}, nil
}

// Select parses body and returns one exact executable preview source.
func Select(body, id string) (Selection, error) {
	return Parse(body).Select(id)
}

type lineRange struct {
	start      int
	contentEnd int
	end        int
}

func splitLines(body string) []lineRange {
	if body == "" {
		return nil
	}
	lines := make([]lineRange, 0, strings.Count(body, "\n")+1)
	for start := 0; start < len(body); {
		newline := strings.IndexByte(body[start:], '\n')
		if newline < 0 {
			lines = append(lines, lineRange{start: start, contentEnd: len(body), end: len(body)})
			break
		}
		end := start + newline + 1
		contentEnd := end - 1
		if contentEnd > start && body[contentEnd-1] == '\r' {
			contentEnd--
		}
		lines = append(lines, lineRange{start: start, contentEnd: contentEnd, end: end})
		start = end
	}
	return lines
}

type fenceOpener struct {
	char     byte
	length   int
	preview  bool
	metadata string
}

func parseOpener(line string) (fenceOpener, bool) {
	index := 0
	for index < len(line) && line[index] == ' ' && index < 4 {
		index++
	}
	if index > 3 || index >= len(line) || (line[index] != '`' && line[index] != '~') {
		return fenceOpener{}, false
	}
	char := line[index]
	runEnd := index
	for runEnd < len(line) && line[runEnd] == char {
		runEnd++
	}
	if runEnd-index < 3 {
		return fenceOpener{}, false
	}
	info := line[runEnd:]
	if char == '`' && strings.Contains(info, "`") {
		return fenceOpener{}, false
	}
	info = strings.TrimSpace(info)
	language, metadata := firstInfoToken(info)
	return fenceOpener{
		char:     char,
		length:   runEnd - index,
		preview:  language == "html-preview",
		metadata: metadata,
	}, true
}

func firstInfoToken(info string) (string, string) {
	for i, r := range info {
		if r == ' ' || r == '\t' {
			return info[:i], strings.TrimSpace(info[i:])
		}
	}
	return info, ""
}

func isCloser(line string, char byte, minimum int) bool {
	index := 0
	for index < len(line) && line[index] == ' ' && index < 4 {
		index++
	}
	if index > 3 || index >= len(line) || line[index] != char {
		return false
	}
	runEnd := index
	for runEnd < len(line) && line[runEnd] == char {
		runEnd++
	}
	if runEnd-index < minimum {
		return false
	}
	for _, value := range line[runEnd:] {
		if value != ' ' && value != '\t' {
			return false
		}
	}
	return true
}

func applyMetadata(descriptor *Descriptor, raw string) {
	if len(raw) > maxInfoSize {
		addDiagnostic(descriptor, "malformed_metadata", fmt.Sprintf("html-preview metadata exceeds %d bytes", maxInfoSize))
		return
	}
	values, err := parseMetadata(raw)
	if err != nil {
		addDiagnostic(descriptor, "malformed_metadata", err.Error())
		return
	}
	for key := range values {
		switch key {
		case "id", "version", "title", "height":
		default:
			addDiagnostic(descriptor, "malformed_metadata", fmt.Sprintf("unknown html-preview metadata key %q", key))
		}
	}

	descriptor.ID = values["id"]
	if descriptor.ID == "" {
		addDiagnostic(descriptor, "missing_id", "html-preview metadata requires id")
	} else if !idPattern.MatchString(descriptor.ID) {
		addDiagnostic(descriptor, "invalid_id", fmt.Sprintf("invalid html-preview id %q", descriptor.ID))
	}

	versionText := values["version"]
	if versionText == "" {
		addDiagnostic(descriptor, "missing_version", "html-preview metadata requires version=1")
	} else {
		version, err := strconv.Atoi(versionText)
		if err != nil || version <= 0 {
			addDiagnostic(descriptor, "malformed_metadata", fmt.Sprintf("invalid html-preview version %q", versionText))
		} else {
			descriptor.Version = version
			if version != 1 {
				addDiagnostic(descriptor, "unknown_version", fmt.Sprintf("unsupported html-preview version %d", version))
			}
		}
	}

	descriptor.Title = values["title"]
	if !utf8.ValidString(descriptor.Title) {
		addDiagnostic(descriptor, "malformed_metadata", "html-preview title is not valid UTF-8")
	} else if utf8.RuneCountInString(descriptor.Title) > 120 {
		addDiagnostic(descriptor, "title_too_long", "html-preview title exceeds 120 Unicode scalar values")
	}

	if heightText := values["height"]; heightText != "" {
		height, err := strconv.Atoi(heightText)
		if err != nil {
			addDiagnostic(descriptor, "malformed_metadata", fmt.Sprintf("invalid html-preview height %q", heightText))
		} else {
			if height < MinHeight {
				height = MinHeight
			}
			if height > MaxHeight {
				height = MaxHeight
			}
			descriptor.Height = height
		}
	}
}

func parseMetadata(raw string) (map[string]string, error) {
	values := map[string]string{}
	for index := 0; ; {
		for index < len(raw) && (raw[index] == ' ' || raw[index] == '\t') {
			index++
		}
		if index == len(raw) {
			return values, nil
		}
		keyStart := index
		for index < len(raw) && isMetadataKeyByte(raw[index]) {
			index++
		}
		if keyStart == index || index >= len(raw) || raw[index] != '=' {
			return nil, fmt.Errorf("malformed html-preview metadata near %q", boundedFragment(raw[keyStart:]))
		}
		key := raw[keyStart:index]
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate html-preview metadata key %q", key)
		}
		index++
		if index == len(raw) {
			return nil, fmt.Errorf("html-preview metadata %q has no value", key)
		}

		var value string
		if raw[index] == '"' || raw[index] == '\'' {
			quote := raw[index]
			index++
			var builder strings.Builder
			closed := false
			for index < len(raw) {
				switch raw[index] {
				case quote:
					index++
					closed = true
				case '\\':
					if index+1 >= len(raw) || (raw[index+1] != quote && raw[index+1] != '\\') {
						return nil, fmt.Errorf("html-preview metadata %q has an invalid escape", key)
					}
					builder.WriteByte(raw[index+1])
					index += 2
				default:
					builder.WriteByte(raw[index])
					index++
				}
				if closed {
					break
				}
			}
			if !closed {
				return nil, fmt.Errorf("html-preview metadata %q has an unclosed quote", key)
			}
			value = builder.String()
			if index < len(raw) && raw[index] != ' ' && raw[index] != '\t' {
				return nil, fmt.Errorf("html-preview metadata %q is not whitespace-delimited", key)
			}
		} else {
			valueStart := index
			for index < len(raw) && raw[index] != ' ' && raw[index] != '\t' {
				index++
			}
			value = raw[valueStart:index]
		}
		if value == "" && key != "title" {
			return nil, fmt.Errorf("html-preview metadata %q has an empty value", key)
		}
		values[key] = value
	}
}

func isMetadataKeyByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-' || value == '_'
}

func boundedFragment(value string) string {
	if len(value) > 32 {
		return value[:32] + "..."
	}
	return value
}

func addDiagnostic(descriptor *Descriptor, code, message string) {
	for _, diagnostic := range descriptor.Diagnostics {
		if diagnostic.Code == code && diagnostic.Message == message {
			return
		}
	}
	descriptor.Diagnostics = append(descriptor.Diagnostics, Diagnostic{Code: code, Message: message})
}
