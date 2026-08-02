package changes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/model"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type authorization interface {
	EvaluateRepository(context.Context, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
	EvaluateRepositoryTx(context.Context, pgx.Tx, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
	EvaluateOrganization(context.Context, authz.Subject, models.OrgScope, authz.Operation) (authz.Decision, error)
	ListReadableRepositories(context.Context, authz.Subject, models.OrgScope) ([]authz.RepositoryAccess, error)
}

type Service struct {
	pool              *pgxpool.Pool
	authz             authorization
	codeChangeLabels  map[string]string
	beforeSnapshot    func()
	afterArtifactLoad func()
}

func New(pool *pgxpool.Pool, authorization *authz.Service, descriptions ...codereview.ProviderDescription) (*Service, error) {
	if pool == nil || authorization == nil {
		return nil, errors.New("changes: database and authorization are required")
	}
	labels := make(map[string]string, len(descriptions))
	for _, description := range descriptions {
		labels[description.ProviderKey] = description.CodeChangeLabel
	}
	return &Service{pool: pool, authz: authorization, codeChangeLabels: labels}, nil
}

func (s *Service) decorateCodeChangeRelationships(items map[uuid.UUID][]models.CodeChangeRelationship) {
	for issueID, relationships := range items {
		for index := range relationships {
			label := strings.TrimSpace(s.codeChangeLabels[relationships[index].ProviderKey])
			if label == "" {
				label = "Code change"
			}
			relationships[index].CodeChangeLabel = label
		}
		items[issueID] = relationships
	}
}

func (s *Service) RepositoryBoard(ctx context.Context, subject authz.Subject, scope models.RepoScope, options ListOptions) (BoardPage, error) {
	if err := validateOptions(&options); err != nil {
		return BoardPage{}, err
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return BoardPage{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return BoardPage{}, err
	}
	return s.loadBoard(ctx, subject, scope.OrgID, []uuid.UUID{scope.RepoID}, options, "", true)
}

func (s *Service) Change(ctx context.Context, subject authz.Subject, scope models.RepoScope, changeKey string) (ChangeCard, string, time.Time, error) {
	key := NormalizeChangeKey(changeKey)
	if key == "" {
		return ChangeCard{}, "", time.Time{}, adminservice.ErrInvalidInput
	}
	options := ListOptions{Page: 1, PerPage: 1}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return ChangeCard{}, "", time.Time{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return ChangeCard{}, "", time.Time{}, err
	}
	page, err := s.loadBoard(ctx, subject, scope.OrgID, []uuid.UUID{scope.RepoID}, options, key, true)
	if err != nil {
		return ChangeCard{}, "", time.Time{}, err
	}
	for _, card := range page.Cards {
		if card.ChangeKey == key {
			return card, page.Validator, page.LastModified, nil
		}
	}
	return ChangeCard{}, "", time.Time{}, adminservice.ErrNotFound
}

func (s *Service) OrganizationBoard(ctx context.Context, subject authz.Subject, scope models.OrgScope, options ListOptions) (BoardPage, error) {
	if err := validateOptions(&options); err != nil {
		return BoardPage{}, err
	}
	decision, err := s.authz.EvaluateOrganization(ctx, subject, scope, authz.OperationReadOrganization)
	if err != nil {
		return BoardPage{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return BoardPage{}, err
	}
	readable, err := s.authz.ListReadableRepositories(ctx, subject, scope)
	if err != nil {
		return BoardPage{}, err
	}
	repoIDs := make([]uuid.UUID, 0, len(readable))
	for _, access := range readable {
		if access.Scope.OrgID == scope.OrgID {
			repoIDs = append(repoIDs, access.Scope.RepoID)
		}
	}
	if len(repoIDs) == 0 {
		return emptyBoard(scope.OrgID, options), nil
	}
	return s.loadBoard(ctx, subject, scope.OrgID, repoIDs, options, "", false)
}

func validateOptions(options *ListOptions) error {
	if options.Page == 0 {
		options.Page = 1
	}
	if options.PerPage == 0 {
		options.PerPage = 20
	}
	if options.Page < 1 || options.PerPage < 1 || options.PerPage > 100 {
		return adminservice.ErrInvalidInput
	}
	if options.Stage != "" && options.Stage != StageUnknown && !knownStage(options.Stage) {
		return adminservice.ErrInvalidInput
	}
	if options.Lifecycle != "" && options.Lifecycle != LifecycleActive && options.Lifecycle != LifecycleBlocked &&
		options.Lifecycle != LifecycleCompleted && options.Lifecycle != LifecycleClosed {
		return adminservice.ErrInvalidInput
	}
	if options.Anomaly != "" && !knownAnomaly(options.Anomaly) {
		return adminservice.ErrInvalidInput
	}
	return nil
}

func knownAnomaly(value string) bool {
	switch value {
	case AnomalyDuplicateArtifactType, AnomalyMarkerLabelMismatch, AnomalyMissingRequiredLinks,
		AnomalyUnsupportedMarkerVersion, AnomalyImplementMissingPredecessor,
		AnomalyOrphanTypedArtifact, AnomalyMalformedIssueMarker, AnomalyCodeChangeBindingMismatch:
		return true
	default:
		return false
	}
}

type repositorySnapshot struct {
	repository Repository
	issues     int64
	comments   int64
	labels     int64
	artifacts  int64
	bindings   int64
	references int64
	updatedAt  time.Time
}

func (s *Service) loadBoard(ctx context.Context, subject authz.Subject, orgID uuid.UUID, repoIDs []uuid.UUID, options ListOptions, changeKey string, strict bool) (BoardPage, error) {
	if s.beforeSnapshot != nil {
		s.beforeSnapshot()
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return BoardPage{}, fmt.Errorf("changes: begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	authorizedIDs := make([]uuid.UUID, 0, len(repoIDs))
	permissions := make(map[uuid.UUID]authz.Permission, len(repoIDs))
	for _, repoID := range repoIDs {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{
			Scope: models.RepoScope{OrgID: orgID, RepoID: repoID}, Operation: authz.OperationRead,
		})
		if err != nil {
			return BoardPage{}, err
		}
		if decision.Allowed {
			authorizedIDs = append(authorizedIDs, repoID)
			permissions[repoID] = decision.EffectivePermission
			continue
		}
		if strict {
			return BoardPage{}, decision.AuthorizationError()
		}
	}
	if len(authorizedIDs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return BoardPage{}, fmt.Errorf("changes: commit empty snapshot: %w", err)
		}
		return emptyBoard(orgID, options), nil
	}
	repositories, err := loadRepositories(ctx, tx, orgID, authorizedIDs)
	if err != nil {
		return BoardPage{}, err
	}
	if strict && len(repositories) != len(authorizedIDs) {
		return BoardPage{}, adminservice.ErrNotFound
	}
	artifacts, diagnostics, err := loadArtifacts(ctx, tx, orgID, authorizedIDs)
	if err != nil {
		return BoardPage{}, err
	}
	if s.afterArtifactLoad != nil {
		s.afterArtifactLoad()
	}
	typed, err := loadTypedArtifacts(ctx, tx, orgID, authorizedIDs)
	if err != nil {
		return BoardPage{}, err
	}
	issueIDs := make([]uuid.UUID, 0, len(artifacts))
	for _, artifact := range artifacts {
		issueIDs = append(issueIDs, artifact.issueID)
	}
	relationships, relationshipModified, err := loadCodeChangeRelationships(ctx, tx, orgID, authorizedIDs, issueIDs, permissions)
	if err != nil {
		return BoardPage{}, err
	}
	s.decorateCodeChangeRelationships(relationships)
	if err := tx.Commit(ctx); err != nil {
		return BoardPage{}, fmt.Errorf("changes: commit snapshot: %w", err)
	}
	cards, diagnosticCounts := projectBoard(orgID, repositories, artifacts, typed, relationships, diagnostics)
	if changeKey != "" {
		matching := cards[:0]
		for _, card := range cards {
			if card.ChangeKey == changeKey {
				matching = append(matching, card)
			}
		}
		cards = matching
	}
	filtered := filterCards(cards, options)
	summary := countCards(filtered)
	total := len(filtered)
	start := (options.Page - 1) * options.PerPage
	if start > total {
		start = total
	}
	end := start + options.PerPage
	if end > total {
		end = total
	}
	// Keep the JSON collection contract stable for an empty filtered page. A
	// nil slice serializes as null, which breaks strict clients that correctly
	// model board cards as an array.
	pageCards := append(make([]ChangeCard, 0, end-start), filtered[start:end]...)
	lastModified := time.Time{}
	for _, repository := range repositories {
		if repository.updatedAt.After(lastModified) {
			lastModified = repository.updatedAt
		}
	}
	if relationshipModified.After(lastModified) {
		lastModified = relationshipModified
	}
	return BoardPage{Cards: pageCards, Page: options.Page, PerPage: options.PerPage, Total: total,
		Counts: summary, Diagnostics: diagnosticCounts, Validator: validator(orgID, repositories, options, changeKey), LastModified: lastModified}, nil
}

func emptyBoard(orgID uuid.UUID, options ListOptions) BoardPage {
	hash := sha256.Sum256([]byte(orgID.String() + optionsFingerprint(options)))
	return BoardPage{Cards: []ChangeCard{}, Page: options.Page, PerPage: options.PerPage,
		Diagnostics: []DiagnosticCount{}, Validator: `"` + hex.EncodeToString(hash[:]) + `"`}
}

func validator(orgID uuid.UUID, repositories map[uuid.UUID]repositorySnapshot, options ListOptions, changeKey string) string {
	ids := make([]uuid.UUID, 0, len(repositories))
	for id := range repositories {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	hash := sha256.New()
	_, _ = hash.Write([]byte(orgID.String()))
	for _, id := range ids {
		repository := repositories[id]
		_, _ = hash.Write([]byte(fmt.Sprintf("|%s:%d:%d:%d:%d:%d:%d", id, repository.issues,
			repository.comments, repository.labels, repository.artifacts, repository.bindings, repository.references)))
	}
	_, _ = hash.Write([]byte(optionsFingerprint(options)))
	_, _ = hash.Write([]byte("|change=" + changeKey))
	return `"` + hex.EncodeToString(hash.Sum(nil)) + `"`
}

func optionsFingerprint(options ListOptions) string {
	return fmt.Sprintf("|%s|%s|%s|%d|%d", options.Stage, options.Lifecycle, options.Anomaly, options.Page, options.PerPage)
}

func filterCards(cards []ChangeCard, options ListOptions) []ChangeCard {
	result := make([]ChangeCard, 0, len(cards))
	for _, card := range cards {
		if options.Stage != "" && card.CurrentStage != options.Stage {
			continue
		}
		if options.Lifecycle != "" && card.Lifecycle != options.Lifecycle {
			continue
		}
		if options.Anomaly != "" && !contains(card.Anomalies, options.Anomaly) {
			continue
		}
		result = append(result, card)
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func countCards(cards []ChangeCard) BoardCounts {
	counts := BoardCounts{Total: len(cards)}
	for _, card := range cards {
		switch card.Lifecycle {
		case LifecycleActive:
			counts.Active++
		case LifecycleBlocked:
			counts.Blocked++
		case LifecycleCompleted:
			counts.Completed++
		case LifecycleClosed:
			counts.Closed++
		}
		switch card.CurrentStage {
		case StageProposal:
			counts.Proposal++
		case StageDesign:
			counts.Design++
		case StageImplement:
			counts.Implement++
		default:
			counts.Unknown++
		}
	}
	return counts
}

func projectBoard(orgID uuid.UUID, repositories map[uuid.UUID]repositorySnapshot, artifacts []rawArtifact,
	typed []typedArtifact, relationships map[uuid.UUID][]models.CodeChangeRelationship,
	diagnostics map[uuid.UUID][]string) ([]ChangeCard, []DiagnosticCount) {
	type groupKey struct {
		repositoryID uuid.UUID
		changeKey    string
	}
	groups := make(map[groupKey][]rawArtifact)
	issueGroups := make(map[uuid.UUID]groupKey)
	for _, artifact := range artifacts {
		key := groupKey{repositoryID: artifact.repositoryID, changeKey: artifact.changeKey}
		groups[key] = append(groups[key], artifact)
		issueGroups[artifact.issueID] = key
	}
	typedByGroup := make(map[groupKey][]typedArtifact)
	for _, item := range typed {
		key, ok := issueGroups[item.issueID]
		if !ok {
			diagnostics[item.repositoryID] = append(diagnostics[item.repositoryID], AnomalyOrphanTypedArtifact)
			continue
		}
		typedByGroup[key] = append(typedByGroup[key], item)
	}
	cards := make([]ChangeCard, 0, len(groups))
	for key, items := range groups {
		card := buildCard(orgID, repositories[key.repositoryID].repository, key.changeKey, items,
			typedByGroup[key], relationships)
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool {
		if !cards[i].UpdatedAt.Equal(cards[j].UpdatedAt) {
			return cards[i].UpdatedAt.After(cards[j].UpdatedAt)
		}
		if strings.ToLower(cards[i].Repository.Name) != strings.ToLower(cards[j].Repository.Name) {
			return strings.ToLower(cards[i].Repository.Name) < strings.ToLower(cards[j].Repository.Name)
		}
		return cards[i].ChangeKey < cards[j].ChangeKey
	})
	counts := make(map[string]int)
	for _, values := range diagnostics {
		for _, code := range dedupeSorted(values) {
			counts[code]++
		}
	}
	for _, card := range cards {
		for _, code := range card.Anomalies {
			counts[code]++
		}
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	result := make([]DiagnosticCount, 0, len(codes))
	for _, code := range codes {
		result = append(result, DiagnosticCount{Code: code, Count: counts[code]})
	}
	return cards, result
}

func buildCard(orgID uuid.UUID, repository Repository, changeKey string, items []rawArtifact,
	typed []typedArtifact, relationships map[uuid.UUID][]models.CodeChangeRelationship) ChangeCard {
	byStage := make(map[Stage][]rawArtifact)
	anomalies := make([]string, 0)
	updatedAt := time.Time{}
	for _, item := range items {
		byStage[item.kind] = append(byStage[item.kind], item)
		if item.updatedAt.After(updatedAt) {
			updatedAt = item.updatedAt
		}
		if !validMarker(item) {
			anomalies = append(anomalies, AnomalyUnsupportedMarkerVersion)
		}
		if knownStage(item.kind) && !matchingArtifactLabel(item) {
			anomalies = append(anomalies, AnomalyMarkerLabelMismatch)
		}
		if validMarker(item) && !hasRequiredLink(item) {
			anomalies = append(anomalies, AnomalyMissingRequiredLinks)
		}
	}
	card := ChangeCard{Repository: repository, ChangeKey: changeKey, CurrentStage: StageUnknown,
		Lifecycle: LifecycleActive, CodeChanges: []models.CodeChangeRelationship{}, Anomalies: []string{}, UpdatedAt: updatedAt}
	for _, stage := range []Stage{StageProposal, StageDesign, StageImplement} {
		stageItems := byStage[stage]
		if len(stageItems) == 0 {
			continue
		}
		if len(stageItems) > 1 {
			anomalies = append(anomalies, AnomalyDuplicateArtifactType)
		}
		selected := selectArtifact(stageItems)
		slot := artifactSlot(selected, orgID)
		switch stage {
		case StageProposal:
			card.Artifacts.Proposal = slot
		case StageDesign:
			card.Artifacts.Design = slot
		case StageImplement:
			card.Artifacts.Implement = slot
			card.CodeChanges = append(card.CodeChanges, relationships[selected.issueID]...)
			for _, relationship := range card.CodeChanges {
				if relationship.SourceBindingMatch == models.SourceBindingMismatched {
					anomalies = append(anomalies, AnomalyCodeChangeBindingMismatch)
				}
			}
		}
		if slot.Valid {
			card.CurrentStage = stage
		}
	}
	if card.Artifacts.Implement != nil && card.Artifacts.Implement.Valid && (card.Artifacts.Design == nil || !card.Artifacts.Design.Valid) {
		anomalies = append(anomalies, AnomalyImplementMissingPredecessor)
	}
	blocked := false
	answers := resolveTypedAnswers(typed)
	for _, item := range typed {
		if item.updatedAt.After(card.UpdatedAt) {
			card.UpdatedAt = item.updatedAt
		}
		switch item.typ {
		case "TASK":
			addProgress(&card.Tasks, item.status)
		case "PROCESS":
			addProgress(&card.Processes, item.status)
		case "QUESTION":
			question := model.TypedComment{Type: "QUESTION", ID: item.key, Status: item.status, Body: item.body}
			blocked = blocked || !model.QuestionIsSatisfied(question, answers)
		}
	}
	anyValid := card.CurrentStage != StageUnknown
	allPresentClosed := anyValid
	for _, slot := range []*Artifact{card.Artifacts.Proposal, card.Artifacts.Design, card.Artifacts.Implement} {
		if slot != nil && slot.Valid && slot.State != "closed" {
			allPresentClosed = false
		}
	}
	switch {
	case blocked:
		card.Lifecycle = LifecycleBlocked
	case allPresentClosed:
		card.Lifecycle = LifecycleClosed
	}
	card.Title = changeKey
	if card.Artifacts.Proposal != nil {
		card.Title = card.Artifacts.Proposal.Title
	} else if card.Artifacts.Implement != nil && card.Artifacts.Implement.Valid {
		card.Title = card.Artifacts.Implement.Title
	} else if card.Artifacts.Design != nil && card.Artifacts.Design.Valid {
		card.Title = card.Artifacts.Design.Title
	}
	card.Anomalies = dedupeSorted(anomalies)
	return card
}
