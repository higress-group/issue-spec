package durable

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type requirementBlock struct {
	Title string
	Body  string
}

type durableDocument struct {
	Capability string
	Prefix     string
	Blocks     []requirementBlock
	Suffix     string
	// Preserve untouched Requirement bytes for section-only projections.
	RequirementsText *string
}

type documentHeading struct {
	Level int
	Start int
	Line  string
	Title string
}

// Only document-level headings delimit the contract. Markdown examples in
// fences, lists and block quotes are content, not durable section identities.
func documentHeadings(body string) []documentHeading {
	// Parsing fragments must not change their exact stored bytes or require a final LF.
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	root := goldmark.DefaultParser().Parse(text.NewReader([]byte(body)))
	var headings []documentHeading
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Lines().Len() == 0 {
			continue
		}
		segment := heading.Lines().At(0)
		start := strings.LastIndex(body[:segment.Start], "\n") + 1
		end := strings.IndexByte(body[start:], '\n') + start
		headings = append(headings, documentHeading{Level: heading.Level, Start: start, Line: body[start:end], Title: string(heading.Text([]byte(body)))})
	}
	return headings
}

func parseDurableDocument(body, capability string) (durableDocument, error) {
	if strings.Contains(body, "\r") || !strings.HasSuffix(body, "\n") {
		return durableDocument{}, errors.New("durable target must use LF newlines and one trailing newline")
	}
	headings := documentHeadings(body)
	if len(headings) == 0 || headings[0].Level != 1 || headings[0].Start != 0 ||
		!strings.HasPrefix(headings[0].Line, "# ") || strings.TrimSpace(strings.TrimPrefix(headings[0].Line, "# ")) == "" {
		return durableDocument{}, errors.New("durable target must start with a non-empty level-one title")
	}
	// Capability identity is validated against the declared path by intent.go;
	// the human-readable title is presentation and must remain untouched.
	purpose, requirements := -1, -1
	for index, heading := range headings {
		if index > 0 && heading.Level == 1 {
			return durableDocument{}, errors.New("durable target must contain one document title")
		}
		switch heading.Line {
		case "## Purpose":
			if purpose != -1 {
				return durableDocument{}, errors.New("durable target has duplicate Purpose sections")
			}
			purpose = index
		case "## Requirements":
			if requirements != -1 {
				return durableDocument{}, errors.New("durable target has duplicate Requirements sections")
			}
			requirements = index
		}
	}
	if purpose == -1 || requirements == -1 || purpose >= requirements {
		return durableDocument{}, errors.New("durable target must contain canonical Purpose then Requirements sections")
	}
	start := headings[requirements].Start + len("## Requirements\n\n")
	if !strings.HasPrefix(body[headings[requirements].Start:], "## Requirements\n\n") {
		return durableDocument{}, errors.New("durable Requirements heading must be followed by a blank line")
	}
	end := len(body)
	var blocks []documentHeading
	for _, heading := range headings[requirements+1:] {
		if heading.Level <= 2 {
			end = heading.Start
			break
		}
		if heading.Level == 3 && strings.HasPrefix(heading.Line, "### Requirement:") {
			blocks = append(blocks, heading)
		}
	}
	requirementsText := body[start:end]
	document := durableDocument{Capability: capability, Prefix: body[:start], Suffix: body[end:], RequirementsText: &requirementsText}
	contentStart := end
	if len(blocks) > 0 {
		contentStart = blocks[0].Start
	}
	if strings.TrimSpace(body[start:contentStart]) != "" {
		return durableDocument{}, errors.New("durable target Requirements section contains content outside Requirement blocks")
	}
	for index, heading := range blocks {
		blockEnd := end
		if index+1 < len(blocks) {
			blockEnd = blocks[index+1].Start
		}
		title := strings.TrimPrefix(heading.Line, "### Requirement: ")
		if title == heading.Line || title == "" || strings.TrimSpace(title) != title {
			return durableDocument{}, errors.New("durable target has an invalid Requirement identity")
		}
		document.Blocks = append(document.Blocks, requirementBlock{Title: title, Body: strings.TrimSpace(body[heading.Start:blockEnd])})
	}
	return document, nil
}

func renderDurableDocument(document durableDocument) string {
	if document.RequirementsText != nil {
		return document.Prefix + *document.RequirementsText + document.Suffix
	}
	if len(document.Blocks) == 0 {
		return document.Prefix + document.Suffix
	}
	blocks := make([]string, len(document.Blocks))
	for index, block := range document.Blocks {
		blocks[index] = strings.TrimSpace(block.Body)
	}
	body := document.Prefix + strings.Join(blocks, "\n\n") + "\n"
	if document.Suffix != "" {
		body += "\n" + document.Suffix
	}
	return body
}

// Section bodies exclude only the surrounding LF delimiters. Horizontal spaces
// and internal newlines are significant preimage bytes, not normalized prose.
func validateSectionBody(name, body string) error {
	if strings.TrimSpace(body) == "" || len(body) > maxTextBytes {
		return fmt.Errorf("%s must contain 1..%d bytes of section content", name, maxTextBytes)
	}
	if strings.Contains(body, "\r") || strings.HasPrefix(body, "\n") || strings.HasSuffix(body, "\n") {
		return fmt.Errorf("%s must use LF and exclude surrounding LF delimiters", name)
	}
	if len(documentHeadings(body)) != 0 {
		return fmt.Errorf("%s must not contain document-level Markdown headings", name)
	}
	return nil
}

func replaceDocumentSection(document *durableDocument, section, before, after string) (string, error) {
	body := renderDurableDocument(*document)
	headings := documentHeadings(body)
	matches := []int{}
	for index, heading := range headings {
		if heading.Level == 2 && heading.Title == section {
			matches = append(matches, index)
		}
	}
	if len(matches) == 0 {
		return BlockTargetMissing, fmt.Errorf("document section %q is missing at baseline", section)
	}
	if len(matches) != 1 {
		return BlockTargetAmbiguous, fmt.Errorf("document section %q is ambiguous at baseline", section)
	}
	index := matches[0]
	heading := headings[index]
	if heading.Line != "## "+section {
		return BlockTargetFileInvalid, fmt.Errorf("document section %q must use its canonical level-two heading", section)
	}
	end := len(body)
	for _, next := range headings[index+1:] {
		if next.Level <= 2 {
			end = next.Start
			break
		}
	}
	// Purpose lives before Requirements; references must be an independent
	// trailing section. Neither operation may reach a Requirement block.
	suffixStart := len(body) - len(document.Suffix)
	if (section == "Purpose" && end > len(document.Prefix)) ||
		(section == "Implementation references" && heading.Start < suffixStart) {
		return BlockTargetFileInvalid, fmt.Errorf("document section %q is not in its admitted position", section)
	}
	start := heading.Start + len(heading.Line) + 1
	for start < end && body[start] == '\n' {
		start++
	}
	for end > start && body[end-1] == '\n' {
		end--
	}
	if body[start:end] != before {
		return BlockProjectionInvalid, fmt.Errorf("document section %q before does not match the exact baseline body", section)
	}
	updatedBody := body[:start] + after + body[end:]
	updatedHeadings := documentHeadings(updatedBody)
	if len(headings) != len(updatedHeadings) {
		return BlockProjectionInvalid, fmt.Errorf("document section %q projection changes document heading boundaries", section)
	}
	for index, original := range headings {
		updated := updatedHeadings[index]
		if original.Level != updated.Level || original.Line != updated.Line || original.Title != updated.Title {
			return BlockProjectionInvalid, fmt.Errorf("document section %q projection changes document heading boundaries", section)
		}
	}
	if _, err := parseDurableDocument(updatedBody, document.Capability); err != nil {
		return BlockProjectionInvalid, fmt.Errorf("document section %q projection invalidates the document: %w", section, err)
	}
	if section == "Purpose" {
		document.Prefix = document.Prefix[:start] + after + document.Prefix[end:]
	} else {
		start -= suffixStart
		end -= suffixStart
		document.Suffix = document.Suffix[:start] + after + document.Suffix[end:]
	}
	return "", nil
}
