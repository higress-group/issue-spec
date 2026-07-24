package jobs

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/preview"
)

const defaultArtifactIssueLimit = 8

var (
	issueURLReferenceRe  = regexp.MustCompile(`(?i)https?://[^\s)]+/issues/([1-9][0-9]*)`)
	issueHashReferenceRe = regexp.MustCompile(`(?:^|[\s(\[])#([1-9][0-9]*)\b`)
)

type IssueSpecArtifactProvider struct {
	GitHub    github.RunnerOperations
	MaxIssues int
}

type answerArtifactCandidate struct {
	artifact    model.Artifact
	observation model.AnswerObservation
}

type answerQuestionIdentity struct {
	issue      int
	sourceURL  string
	questionID string
	scope      string
}

func (p *IssueSpecArtifactProvider) ArtifactsForJob(ctx context.Context, job state.Job) ([]model.Artifact, error) {
	if p == nil || p.GitHub == nil {
		return nil, fmt.Errorf("issue-spec artifact provider GitHub backend is required")
	}
	repo := strings.TrimSpace(job.Repo)
	if repo == "" {
		return nil, fmt.Errorf("job repo is required for context artifacts")
	}
	if job.IssueNumber <= 0 {
		return nil, fmt.Errorf("job issue number is required for context artifacts")
	}

	maxIssues := p.MaxIssues
	if maxIssues <= 0 {
		maxIssues = defaultArtifactIssueLimit
	}
	queue := []int{job.IssueNumber}
	queued := map[int]bool{job.IssueNumber: true}
	visited := map[int]bool{}
	seenArtifacts := map[string]bool{}
	var artifacts []model.Artifact
	var answerCandidates []answerArtifactCandidate

	enqueue := func(issue int) {
		if issue <= 0 || queued[issue] || len(queued) >= maxIssues {
			return
		}
		queued[issue] = true
		queue = append(queue, issue)
	}
	addArtifact := func(artifact model.Artifact) {
		key := artifactKey(artifact)
		if seenArtifacts[key] {
			return
		}
		seenArtifacts[key] = true
		artifacts = append(artifacts, artifact)
	}

	for len(queue) > 0 {
		issueNumber := queue[0]
		queue = queue[1:]
		if visited[issueNumber] {
			continue
		}
		visited[issueNumber] = true

		issueResult, err := p.GitHub.GetIssueContext(ctx, repo, issueNumber, github.ConditionalRequest{})
		if err != nil {
			return nil, fmt.Errorf("load issue context %s#%d: %w", repo, issueNumber, err)
		}
		issue := issueResult.Issue
		if issue.Number == 0 {
			issue.Number = issueNumber
		}
		addArtifact(issueContextArtifact(repo, issue))
		for _, ref := range issueReferencesFromText(issue.Body) {
			enqueue(ref)
		}

		comments, err := p.issueComments(ctx, repo, issueNumber)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			tc := model.ParseTypedComment(comment.Body)
			if strings.TrimSpace(tc.Type) == "" || strings.TrimSpace(tc.ID) == "" {
				continue
			}
			if comment.IssueNumber == 0 {
				comment.IssueNumber = issueNumber
			}
			artifact := model.Artifact{
				Issue:     comment.IssueNumber,
				CommentID: comment.ID,
				URL:       comment.HTMLURL,
				APIURL:    comment.URL,
				Comment:   tc,
			}
			if tc.Type == "ANSWER" {
				actor := ""
				if comment.User != nil {
					actor = comment.User.Login
				}
				answerCandidates = append(answerCandidates, answerArtifactCandidate{
					artifact: artifact,
					observation: model.AnswerObservation{
						ProviderID: strconv.FormatInt(comment.ID, 10),
						Actor:      actor,
						CreatedAt:  comment.CreatedAt,
						UpdatedAt:  comment.UpdatedAt,
						Body:       comment.Body,
						URL:        comment.HTMLURL,
					},
				})
				// ANSWER history is not graph-discovery authority. Only the
				// effective ANSWER for an already selected QUESTION enters
				// Agent context below.
				continue
			}
			addArtifact(artifact)
			for _, ref := range issueReferencesFromTypedComment(tc) {
				enqueue(ref)
			}
		}
	}

	for _, answer := range selectEffectiveAnswerArtifacts(artifacts, answerCandidates) {
		addArtifact(answer)
	}
	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifactKey(artifacts[i]) < artifactKey(artifacts[j])
	})
	return artifacts, nil
}

func selectEffectiveAnswerArtifacts(artifacts []model.Artifact, candidates []answerArtifactCandidate) []model.Artifact {
	questions := make([]model.Artifact, 0)
	for _, artifact := range artifacts {
		if artifact.Comment.Type != "QUESTION" || len(artifact.Comment.Errors) > 0 {
			continue
		}
		if _, found, err := model.ParseChoiceModel(artifact.Comment.Body); err == nil && found {
			questions = append(questions, artifact)
		}
	}

	type eligibleAnswer struct {
		candidate answerArtifactCandidate
		identity  answerQuestionIdentity
	}
	eligible := make([]eligibleAnswer, 0, len(candidates))
	observationsByQuestion := map[answerQuestionIdentity][]model.AnswerObservation{}
	for _, candidate := range candidates {
		payload, err := model.ParseAnswerPayload(candidate.observation.Body)
		if err != nil {
			continue
		}
		matches := 0
		var identity answerQuestionIdentity
		for _, question := range questions {
			if answerMatchesQuestion(candidate.artifact, payload, question) {
				matches++
				identity = answerQuestionIdentity{
					issue:      question.Issue,
					sourceURL:  model.NormalizeURL(question.URL),
					questionID: question.Comment.ID,
					scope:      candidate.artifact.Comment.Scope,
				}
			}
		}
		if matches != 1 {
			continue
		}
		eligible = append(eligible, eligibleAnswer{candidate: candidate, identity: identity})
		observationsByQuestion[identity] = append(observationsByQuestion[identity], candidate.observation)
	}

	effectiveByQuestion := make(map[answerQuestionIdentity]model.ResolvedAnswer, len(observationsByQuestion))
	for identity, observations := range observationsByQuestion {
		resolution := model.ResolveEffectiveAnswers(observations)
		if effective, ok := resolution.Effective[identity.questionID]; ok {
			effectiveByQuestion[identity] = effective
		}
	}
	selected := make([]model.Artifact, 0, len(effectiveByQuestion))
	for _, answer := range eligible {
		effective, ok := effectiveByQuestion[answer.identity]
		if !ok ||
			effective.ProviderID != answer.candidate.observation.ProviderID ||
			effective.BodyDigest != model.RepresentationDigest(answer.candidate.observation.Body) {
			continue
		}
		selected = append(selected, answer.candidate.artifact)
	}
	return selected
}

func answerMatchesQuestion(answer model.Artifact, payload model.AnswerPayload, question model.Artifact) bool {
	if answer.Issue != question.Issue ||
		answer.Comment.Scope != payload.Question.ID ||
		payload.Question.ID != question.Comment.ID {
		return false
	}
	if model.NormalizeURL(payload.Question.SourceURL) != model.NormalizeURL(question.URL) {
		return false
	}
	return model.NormalizeURL(payload.Question.IssueURL) == issueURLForArtifact(question.URL)
}

func issueURLForArtifact(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() {
		return ""
	}
	parsed.Fragment = ""
	return model.NormalizeURL(parsed.String())
}

func (p *IssueSpecArtifactProvider) issueComments(ctx context.Context, repo string, issueNumber int) ([]github.Comment, error) {
	var all []github.Comment
	page := github.RunnerPageOptions{}
	for {
		result, err := p.GitHub.ListIssueCommentsPage(ctx, repo, issueNumber, github.CommentListOptions{Page: page})
		if err != nil {
			return nil, fmt.Errorf("load issue comments %s#%d: %w", repo, issueNumber, err)
		}
		all = append(all, result.Comments...)
		if result.Metadata.Pagination.NextURL == "" {
			return all, nil
		}
		page = github.RunnerPageOptions{CursorURL: result.Metadata.Pagination.NextURL}
	}
}

func issueContextArtifact(repo string, issue github.Issue) model.Artifact {
	number := issue.Number
	body := strings.TrimSpace(issue.Body)
	content := fmt.Sprintf("Issue: #%d\nTitle: %s\nState: %s\nURL: %s", number, issue.Title, issue.State, issue.HTMLURL)
	if body != "" {
		expansionBase := runnercontext.IssueReadExpansionBase(repo, number, 0, issue.HTMLURL)
		folded, _ := runnercontext.FoldPreviews(body, issue.HTMLURL, expansionBase)
		content += "\n\n" + folded
	}
	return model.Artifact{
		Issue:  number,
		URL:    issue.HTMLURL,
		APIURL: issue.URL,
		Comment: model.TypedComment{
			Type:   "ISSUE_CONTEXT",
			ID:     fmt.Sprintf("ISSUE-%03d", number),
			Status: issue.State,
			Scope:  "issue-context",
			Body:   content,
		},
	}
}

func issueReferencesFromTypedComment(tc model.TypedComment) []int {
	var refs []int
	for _, linkName := range []string{"Proposal Issue", "Design Issue", "Implement Issue", "Related Comments"} {
		for _, value := range model.LinkValues(tc, linkName) {
			if issue, ok := issueNumberFromReference(value); ok {
				refs = append(refs, issue)
			}
		}
	}
	refs = append(refs, issueReferencesFromText(tc.Body)...)
	return uniqueIssueNumbers(refs)
}

func issueReferencesFromText(text string) []int {
	text = previewOpaqueView(text)
	var refs []int
	for _, match := range issueURLReferenceRe.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		if issue, err := strconv.Atoi(match[1]); err == nil {
			refs = append(refs, issue)
		}
	}
	for _, match := range issueHashReferenceRe.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		if issue, err := strconv.Atoi(match[1]); err == nil {
			refs = append(refs, issue)
		}
	}
	return uniqueIssueNumbers(refs)
}

func previewOpaqueView(text string) string {
	parsed := preview.Parse(text)
	if len(parsed.Descriptors) == 0 {
		return text
	}
	out := []byte(text)
	for _, descriptor := range parsed.Descriptors {
		for i := descriptor.Range.Start; i < descriptor.Range.End; i++ {
			if out[i] != '\r' && out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return string(out)
}

func issueNumberFromReference(raw string) (int, bool) {
	value := strings.Trim(strings.TrimSpace(raw), ".,;)")
	if strings.HasPrefix(value, "#") {
		n, err := strconv.Atoi(strings.TrimPrefix(value, "#"))
		return n, err == nil && n > 0
	}
	n, err := github.ParseIssueNumber(value)
	return n, err == nil && n > 0
}

func uniqueIssueNumbers(values []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func artifactKey(artifact model.Artifact) string {
	return fmt.Sprintf("%09d/%012d/%s/%s/%s", artifact.Issue, artifact.CommentID, artifact.Comment.Type, artifact.Comment.ID, artifact.URL)
}
