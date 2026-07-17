// Package mentions resolves canonical @login tokens from Markdown prose.
package mentions

import (
	"bytes"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	goldmarktext "github.com/yuin/goldmark/text"
)

const MaxLoginRunes = 64

// Parser uses Goldmark's GFM parser and inspects only prose text nodes. Code,
// links, autolinks, URLs recognized by GFM linkify, and email autolinks are
// excluded by their AST ancestry rather than by rewriting the source.
type Parser struct{ markdown goldmark.Markdown }

func NewParser() *Parser {
	return &Parser{markdown: goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)}
}

// Logins returns lower-cased, de-duplicated canonical login candidates in a
// deterministic order. Exact identity and account state remain server-side
// database decisions made by the projector.
func (p *Parser) Logins(source []byte) []string {
	if p == nil || p.markdown == nil || len(source) == 0 {
		return nil
	}
	root := p.markdown.Parser().Parse(goldmarktext.NewReader(source))
	unique := make(map[string]struct{})
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node.Kind() != ast.KindText || excludedText(node) {
			return ast.WalkContinue, nil
		}
		text, ok := node.(*ast.Text)
		if !ok {
			return ast.WalkContinue, nil
		}
		for _, login := range proseLogins(text.Segment.Value(source)) {
			unique[login] = struct{}{}
		}
		return ast.WalkContinue, nil
	})
	result := make([]string, 0, len(unique))
	for login := range unique {
		result = append(result, login)
	}
	sort.Strings(result)
	return result
}

func excludedText(node ast.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case ast.KindLink, ast.KindAutoLink, ast.KindCodeSpan, ast.KindCodeBlock, ast.KindFencedCodeBlock:
			return true
		}
	}
	return false
}

func proseLogins(text []byte) []string {
	var result []string
	for index := 0; index < len(text); {
		at := bytes.IndexByte(text[index:], '@')
		if at < 0 {
			break
		}
		at += index
		if at > 0 {
			prior, _ := utf8.DecodeLastRune(text[:at])
			if unicode.IsLetter(prior) || unicode.IsDigit(prior) || prior == '_' || prior == '@' || prior == '/' {
				index = at + 1
				continue
			}
		}
		end := at + 1
		for end < len(text) && canonicalLoginByte(text[end]) {
			end++
		}
		candidate := text[at+1 : end]
		if len(candidate) == 0 || len(candidate) > MaxLoginRunes || candidate[0] == '-' || candidate[len(candidate)-1] == '-' {
			index = max(end, at+1)
			continue
		}
		if end < len(text) && (text[end] == '_' || text[end] >= utf8.RuneSelf) {
			index = end
			continue
		}
		result = append(result, strings.ToLower(string(candidate)))
		index = end
	}
	return result
}

func canonicalLoginByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-'
}
