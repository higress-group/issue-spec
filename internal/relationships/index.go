package relationships

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
)

const (
	DefaultIdentityLimit = 10
	defaultArtifactLimit = 512
	defaultPhysicalLimit = 4096
)

// Classification describes one physical Related Comments link. Only canonical
// edges enter forward/reverse adjacency.
type Classification string

const (
	Canonical      Classification = "canonical"
	LegacyBacklink Classification = "legacy_backlink"
	LegacyOrStale  Classification = "legacy_or_stale"
	Unknown        Classification = "unknown"
)

// Edge is topology only. Assignment, receipt, selector, provenance, assurance,
// and evidence eligibility deliberately do not appear in this structure.
type Edge struct {
	Kind           Kind              `json:"kind,omitempty"`
	Owner          model.ArtifactRef `json:"owner"`
	Target         model.ArtifactRef `json:"target"`
	Classification Classification    `json:"classification"`
	PhysicalSource model.ArtifactRef `json:"physical_source"`
	PhysicalURL    string            `json:"physical_url"`
}

type DetailAction struct {
	CommandFamily string   `json:"command_family"`
	Arguments     []string `json:"arguments"`
}

// Adjacency is one deterministic, independently capped relationship view.
type Adjacency struct {
	Kind      Kind              `json:"kind"`
	Artifact  model.ArtifactRef `json:"artifact"`
	Edges     []Edge            `json:"edges"`
	Total     int               `json:"total"`
	Truncated bool              `json:"truncated"`
	Detail    DetailAction      `json:"detail"`
}

type Totals struct {
	Artifacts      int `json:"artifacts"`
	PhysicalLinks  int `json:"physical_links"`
	Canonical      int `json:"canonical"`
	LegacyBacklink int `json:"legacy_backlink"`
	LegacyOrStale  int `json:"legacy_or_stale"`
	Unknown        int `json:"unknown"`
}

// Index contains the complete bounded canonical edge set, deterministic
// capped views, and an audit classification for every physical link.
type Index struct {
	Edges           []Edge      `json:"edges"`
	Forward         []Adjacency `json:"forward"`
	Reverse         []Adjacency `json:"reverse"`
	Classifications []Edge      `json:"classifications"`
	Totals          Totals      `json:"totals"`
	IdentityLimit   int         `json:"identity_limit"`
	Truncated       bool        `json:"truncated"`
}

type BuildOptions struct {
	IdentityLimit     int
	ArtifactLimit     int
	PhysicalLinkLimit int
}

type catalog struct {
	artifacts map[string]model.Artifact
	refs      map[string]model.ArtifactRef
	byID      map[string]string
	byURL     map[string]string
}

type physicalLink struct {
	source     model.ArtifactRef
	target     model.ArtifactRef
	url        string
	resolved   bool
	candidates []OwnerRule
}

// BuildIndex is pure: it performs no provider reads, writes, mutation, cache,
// or evidence selection. The optional argument exists only to tighten bounds.
func BuildIndex(artifacts []model.Artifact, options ...BuildOptions) (Index, error) {
	opts, err := normalizeBuildOptions(options)
	if err != nil {
		return Index{}, err
	}
	if len(artifacts) > opts.ArtifactLimit {
		return Index{}, fmt.Errorf("%w: artifacts=%d limit=%d", ErrBound, len(artifacts), opts.ArtifactLimit)
	}
	set, err := buildCatalog(artifacts)
	if err != nil {
		return Index{}, err
	}
	links, err := collectPhysicalLinks(set, opts.PhysicalLinkLimit)
	if err != nil {
		return Index{}, err
	}

	canonicalByKey := map[string]Edge{}
	classifications := make([]Edge, 0, len(links))
	pending := make([]physicalLink, 0, len(links))
	for _, link := range links {
		if !link.resolved || len(link.candidates) == 0 {
			classifications = append(classifications, unknownEdge(link))
			continue
		}
		var exact []Edge
		for _, rule := range link.candidates {
			for _, pair := range orientations(rule, link.source, link.target) {
				if pair.owner.Key() != link.source.Key() || !semanticAuthority(set, rule, pair.owner, pair.target) {
					continue
				}
				exact = append(exact, Edge{Kind: rule.Kind, Owner: pair.owner, Target: pair.target, Classification: Canonical,
					PhysicalSource: link.source, PhysicalURL: link.url})
			}
		}
		if len(exact) > 1 {
			return Index{}, fmt.Errorf("%w: physical link %s -> %s matches multiple owner rules", ErrAmbiguous,
				link.source.ID, link.target.ID)
		}
		if len(exact) == 0 {
			pending = append(pending, link)
			continue
		}
		edge := exact[0]
		key := edgeKey(edge.Kind, edge.Owner, edge.Target)
		if previous, exists := canonicalByKey[key]; !exists || edge.PhysicalURL < previous.PhysicalURL {
			canonicalByKey[key] = edge
		}
		classifications = append(classifications, edge)
	}

	for _, link := range pending {
		var reverse []Edge
		for _, rule := range link.candidates {
			for _, pair := range orientations(rule, link.source, link.target) {
				if pair.target.Key() != link.source.Key() {
					continue
				}
				if canonical, ok := canonicalByKey[edgeKey(rule.Kind, pair.owner, pair.target)]; ok {
					reverse = append(reverse, Edge{Kind: rule.Kind, Owner: canonical.Owner, Target: canonical.Target,
						Classification: LegacyBacklink, PhysicalSource: link.source, PhysicalURL: link.url})
				}
			}
		}
		if len(reverse) > 1 {
			return Index{}, fmt.Errorf("%w: backlink %s -> %s matches multiple canonical edges", ErrAmbiguous,
				link.source.ID, link.target.ID)
		}
		if len(reverse) == 1 {
			classifications = append(classifications, reverse[0])
			continue
		}
		edge := Edge{Owner: link.source, Target: link.target, Classification: LegacyOrStale,
			PhysicalSource: link.source, PhysicalURL: link.url}
		if len(link.candidates) == 1 && link.candidates[0].OwnerType != link.candidates[0].TargetType {
			edge.Kind = link.candidates[0].Kind
			pairs := orientations(link.candidates[0], link.source, link.target)
			edge.Owner, edge.Target = pairs[0].owner, pairs[0].target
		}
		classifications = append(classifications, edge)
	}

	edges := make([]Edge, 0, len(canonicalByKey))
	for _, edge := range canonicalByKey {
		edges = append(edges, edge)
	}
	sortEdges(edges)
	sortEdges(classifications)
	index := Index{Edges: edges, Classifications: classifications, IdentityLimit: opts.IdentityLimit,
		Totals: Totals{Artifacts: len(set.refs), PhysicalLinks: len(classifications)}}
	for _, edge := range classifications {
		switch edge.Classification {
		case Canonical:
			index.Totals.Canonical++
		case LegacyBacklink:
			index.Totals.LegacyBacklink++
		case LegacyOrStale:
			index.Totals.LegacyOrStale++
		case Unknown:
			index.Totals.Unknown++
		}
	}
	index.Forward, index.Reverse = buildAdjacency(edges, opts.IdentityLimit)
	for _, view := range append(append([]Adjacency(nil), index.Forward...), index.Reverse...) {
		index.Truncated = index.Truncated || view.Truncated
	}
	return index, nil
}

func normalizeBuildOptions(values []BuildOptions) (BuildOptions, error) {
	if len(values) > 1 {
		return BuildOptions{}, fmt.Errorf("%w: at most one options value is allowed", ErrInvalid)
	}
	opts := BuildOptions{}
	if len(values) == 1 {
		opts = values[0]
	}
	if opts.IdentityLimit < 0 || opts.ArtifactLimit < 0 || opts.PhysicalLinkLimit < 0 {
		return BuildOptions{}, fmt.Errorf("%w: bounds cannot be negative", ErrInvalid)
	}
	if opts.IdentityLimit == 0 {
		opts.IdentityLimit = DefaultIdentityLimit
	}
	if opts.ArtifactLimit == 0 {
		opts.ArtifactLimit = defaultArtifactLimit
	}
	if opts.PhysicalLinkLimit == 0 {
		opts.PhysicalLinkLimit = defaultPhysicalLimit
	}
	return opts, nil
}

func buildCatalog(artifacts []model.Artifact) (catalog, error) {
	set := catalog{artifacts: map[string]model.Artifact{}, refs: map[string]model.ArtifactRef{},
		byID: map[string]string{}, byURL: map[string]string{}}
	proposalIssues, implementIssues := map[int]bool{}, map[int]bool{}
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Comment.ID) == "" {
			continue
		}
		ref, err := artifact.Ref()
		if err != nil {
			return catalog{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if previous, exists := set.byID[ref.ID]; exists {
			return catalog{}, fmt.Errorf("%w: duplicate typed id %s on %s and %s", ErrAmbiguous, ref.ID,
				set.refs[previous].URL, ref.URL)
		}
		if err := validateURLIssue(ref); err != nil {
			return catalog{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		key := ref.Key()
		set.byID[ref.ID], set.refs[key], set.artifacts[key] = key, ref, artifact
		for _, alias := range model.ArtifactProviderURLs(artifact) {
			aliasRef := ref
			aliasRef.URL = alias
			if err := aliasRef.Validate(); err != nil {
				return catalog{}, fmt.Errorf("%w: artifact %s provider alias: %v", ErrInvalid, ref.ID, err)
			}
			if err := validateURLIssue(aliasRef); err != nil {
				return catalog{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			if previous, exists := set.byURL[alias]; exists && previous != key {
				return catalog{}, fmt.Errorf("%w: provider url %s resolves to both %s and %s", ErrAmbiguous,
					alias, set.refs[previous].ID, ref.ID)
			}
			set.byURL[alias] = key
		}
		switch ref.Type {
		case "SPEC":
			proposalIssues[ref.Issue] = true
		case "TASK", "PROCESS", "REVIEW", "VERIFY":
			implementIssues[ref.Issue] = true
		}
	}
	if len(proposalIssues) > 1 {
		return catalog{}, fmt.Errorf("%w: SPEC artifacts span multiple proposal issues", ErrInvalid)
	}
	if len(implementIssues) > 1 {
		return catalog{}, fmt.Errorf("%w: TASK/PROCESS/REVIEW/VERIFY artifacts span multiple implement issues", ErrInvalid)
	}
	for proposal := range proposalIssues {
		if implementIssues[proposal] {
			return catalog{}, fmt.Errorf("%w: proposal and implement artifacts share issue %d", ErrInvalid, proposal)
		}
	}
	return set, nil
}

func validateURLIssue(ref model.ArtifactRef) error {
	parsed, err := url.Parse(ref.URL)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] != "issues" {
			continue
		}
		number, parseErr := strconv.Atoi(parts[index+1])
		if parseErr == nil && number != ref.Issue {
			return fmt.Errorf("artifact %s URL belongs to issue %d, not %d", ref.ID, number, ref.Issue)
		}
		return nil
	}
	return nil
}

func collectPhysicalLinks(set catalog, limit int) ([]physicalLink, error) {
	keys := make([]string, 0, len(set.artifacts))
	for key := range set.artifacts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var links []physicalLink
	for _, key := range keys {
		artifact, source := set.artifacts[key], set.refs[key]
		seen := map[string]bool{}
		for _, raw := range artifact.Comment.Links[RelatedCommentsField] {
			value := model.NormalizeURL(raw)
			if value == "" {
				continue
			}
			if seen[value] {
				return nil, fmt.Errorf("%w: %s repeats related URL %s", ErrAmbiguous, source.ID, value)
			}
			seen[value] = true
			if len(links) == limit {
				return nil, fmt.Errorf("%w: physical links exceed %d", ErrBound, limit)
			}
			link := physicalLink{source: source, url: value}
			if targetKey, ok := set.byURL[value]; ok {
				link.target, link.resolved = set.refs[targetKey], true
				if link.target.Key() != source.Key() {
					link.candidates = candidateRules(source.Type, link.target.Type)
				}
			} else {
				link.target = model.ArtifactRef{URL: value}
			}
			links = append(links, link)
		}
	}
	return links, nil
}

func semanticAuthority(set catalog, rule OwnerRule, owner, target model.ArtifactRef) bool {
	artifact := set.artifacts[owner.Key()]
	contains := func(headings ...string) bool {
		for _, heading := range headings {
			for _, value := range model.TypedSectionList(artifact.Comment.Body, heading) {
				if value == target.ID {
					return true
				}
			}
		}
		return false
	}
	switch rule.Kind {
	case TaskCoversSpec:
		return contains("### Covers")
	case ProcessParentTask:
		values := model.TypedSectionList(artifact.Comment.Body, "### Parent TASK")
		return len(values) == 1 && values[0] == target.ID
	case ProcessDependsProcess:
		return contains("### Dependencies")
	case ProcessSupersededBy:
		value, found, err := model.ParseSupersededBy(artifact.Comment.Body, owner.ID)
		return err == nil && found && value.ProcessID == target.ID && model.NormalizeURL(value.URL) == target.URL
	case ReviewCoversProcess:
		return contains("### Covered PROCESSes", "### Review PROCESS", "### Covers") ||
			acceptedReviewProcessID(artifact.Comment.Body) == target.ID
	case ReviewCoversSpec:
		if contains("### Covered SPECs", "### Covers") {
			return true
		}
		processID := acceptedReviewProcessID(artifact.Comment.Body)
		processKey, ok := set.byID[processID]
		if !ok || set.refs[processKey].Type != "PROCESS" {
			return false
		}
		for _, value := range model.TypedSectionList(set.artifacts[processKey].Comment.Body, "### Covers") {
			if value == target.ID {
				return true
			}
		}
		return false
	case VerifyCoversProcess:
		if contains("### Covered PROCESSes", "### Verification PROCESS", "### Covers") {
			return true
		}
		return hasAcceptedVerification(artifact.Comment.Body) && soleRelatedTarget(set, artifact, "PROCESS") == target.ID
	case VerifyCoversSpec:
		return contains("### Covered SPECs", "### Covers")
	default:
		return false
	}
}

func acceptedReviewProcessID(body string) string {
	const start = "<!-- issue-spec:accepted-review-receipt version=2 -->"
	const end = "<!-- /issue-spec:accepted-review-receipt -->"
	if _, found, err := model.ObserveAcceptedReceiptAuthority(body, assignment.RoleReview); err != nil || !found {
		return ""
	}
	if strings.Count(body, start) != 1 || strings.Count(body, end) != 1 {
		return ""
	}
	left, right := strings.Index(body, start)+len(start), strings.Index(body, end)
	if right <= left {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(body[left:right])), &fields) != nil {
		return ""
	}
	var processID string
	if json.Unmarshal(fields["assignment_process_id"], &processID) != nil ||
		model.ValidateTypedIdentity("PROCESS", processID) != nil {
		return ""
	}
	return processID
}

func hasAcceptedVerification(body string) bool {
	_, found, err := model.ObserveAcceptedReceiptAuthority(body, assignment.RoleVerification)
	return err == nil && found
}

func soleRelatedTarget(set catalog, artifact model.Artifact, targetType string) string {
	seen := map[string]bool{}
	for _, raw := range artifact.Comment.Links[RelatedCommentsField] {
		if key, ok := set.byURL[model.NormalizeURL(raw)]; ok && set.refs[key].Type == targetType {
			seen[set.refs[key].ID] = true
		}
	}
	if len(seen) != 1 {
		return ""
	}
	for id := range seen {
		return id
	}
	return ""
}

type orientedPair struct{ owner, target model.ArtifactRef }

func orientations(rule OwnerRule, left, right model.ArtifactRef) []orientedPair {
	if rule.OwnerType == rule.TargetType && left.Type == rule.OwnerType && right.Type == rule.TargetType {
		return []orientedPair{{left, right}, {right, left}}
	}
	if left.Type == rule.OwnerType && right.Type == rule.TargetType {
		return []orientedPair{{left, right}}
	}
	return []orientedPair{{right, left}}
}

func unknownEdge(link physicalLink) Edge {
	return Edge{Owner: link.source, Target: link.target, Classification: Unknown,
		PhysicalSource: link.source, PhysicalURL: link.url}
}

func edgeKey(kind Kind, owner, target model.ArtifactRef) string {
	return string(kind) + "\x00" + owner.Key() + "\x00" + target.Key()
}

func sortEdges(edges []Edge) {
	sort.Slice(edges, func(i, j int) bool {
		left := string(edges[i].Classification) + "\x00" + edgeKey(edges[i].Kind, edges[i].Owner, edges[i].Target) +
			"\x00" + edges[i].PhysicalSource.Key() + "\x00" + edges[i].PhysicalURL
		right := string(edges[j].Classification) + "\x00" + edgeKey(edges[j].Kind, edges[j].Owner, edges[j].Target) +
			"\x00" + edges[j].PhysicalSource.Key() + "\x00" + edges[j].PhysicalURL
		return left < right
	})
}

func buildAdjacency(edges []Edge, limit int) ([]Adjacency, []Adjacency) {
	type group struct {
		kind  Kind
		ref   model.ArtifactRef
		edges []Edge
	}
	forwardGroups, reverseGroups := map[string]*group{}, map[string]*group{}
	for _, edge := range edges {
		forwardKey := string(edge.Kind) + "\x00" + edge.Owner.Key()
		if forwardGroups[forwardKey] == nil {
			forwardGroups[forwardKey] = &group{kind: edge.Kind, ref: edge.Owner}
		}
		forwardGroups[forwardKey].edges = append(forwardGroups[forwardKey].edges, edge)
		reverseKey := string(edge.Kind) + "\x00" + edge.Target.Key()
		if reverseGroups[reverseKey] == nil {
			reverseGroups[reverseKey] = &group{kind: edge.Kind, ref: edge.Target}
		}
		reverseGroups[reverseKey].edges = append(reverseGroups[reverseKey].edges, edge)
	}
	build := func(groups map[string]*group, direction string) []Adjacency {
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		views := make([]Adjacency, 0, len(keys))
		for _, key := range keys {
			value := groups[key]
			sortEdges(value.edges)
			view := Adjacency{Kind: value.kind, Artifact: value.ref, Total: len(value.edges),
				Detail: DetailAction{CommandFamily: "relationship-detail", Arguments: []string{
					"--direction", direction, "--kind", string(value.kind), "--artifact", value.ref.ID,
				}}}
			count := len(value.edges)
			if count > limit {
				count, view.Truncated = limit, true
			}
			view.Edges = append([]Edge(nil), value.edges[:count]...)
			views = append(views, view)
		}
		return views
	}
	return build(forwardGroups, "forward"), build(reverseGroups, "reverse")
}
