package durable

import (
	"errors"
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
}

type documentHeading struct {
	Level int
	Start int
	Line  string
}

// Only document-level headings delimit the contract. Markdown examples in
// fences, lists and block quotes are content, not durable section identities.
func documentHeadings(body string) []documentHeading {
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
		headings = append(headings, documentHeading{Level: heading.Level, Start: start, Line: body[start:end]})
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
	document := durableDocument{Capability: capability, Prefix: body[:start], Suffix: body[end:]}
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
