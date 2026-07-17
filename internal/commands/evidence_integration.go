package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	coreevidence "github.com/higress-group/issue-spec/internal/evidence"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/workflow"
)

const nativeEvidenceSyncResponseLimit = int64(4 << 20)

type nativeEvidenceProvider interface {
	ResolveTarget(context.Context, string, int, string) (coreevidence.NativeTarget, error)
	UpsertArchiveReference(context.Context, coreevidence.NativeTarget, codereview.Reference, string, string, string) error
	SynchronizeSnapshot(context.Context, coreevidence.NativeTarget, codereview.Snapshot) error
}

func defaultNewNativeEvidenceProvider(profile auth.Profile, token string) (nativeEvidenceProvider, error) {
	native, err := coreevidence.NewNativeProvider(profile, token)
	if err != nil {
		return nil, err
	}
	api, err := github.NewClientWithOptions(github.ClientOptions{Host: profile.Hostname, BaseURL: profile.NativeAPIURL,
		Token: token, CAFile: profile.CAFile})
	if err != nil {
		return nil, err
	}
	httpClient, ok := api.HTTPClient.(*http.Client)
	if !ok {
		return nil, errors.New("native evidence client requires an HTTP client")
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("native evidence redirects are forbidden")
	}
	return &commandNativeEvidenceClient{NativeProvider: native, api: api}, nil
}

type commandNativeEvidenceClient struct {
	*coreevidence.NativeProvider
	api *github.Client
}

type externalEvidenceConsumption struct {
	ProviderKey        string                    `json:"provider_key"`
	ExternalRepository string                    `json:"external_repository"`
	ChangeID           string                    `json:"change_id"`
	ReferenceVersion   int64                     `json:"reference_version"`
	SubjectRevision    string                    `json:"subject_revision"`
	EvidenceIDs        []string                  `json:"evidence_ids"`
	Bindings           []externalEvidenceBinding `json:"bindings,omitempty"`
}

type externalEvidenceBinding struct {
	ProcessID       string                  `json:"process_id"`
	SpecID          string                  `json:"spec_id"`
	EvidenceID      string                  `json:"evidence_id"`
	Kind            codereview.EvidenceKind `json:"kind"`
	SubjectRevision string                  `json:"subject_revision"`
	Trusted         bool                    `json:"trusted"`
	Source          string                  `json:"source"`
}

var (
	externalProcessIDPattern = regexp.MustCompile(`^PROCESS-[0-9]{3,}$`)
	externalSpecIDPattern    = regexp.MustCompile(`^SPEC-[0-9]{3,}$`)
)

type externalGateResult struct {
	Consumption externalEvidenceConsumption `json:"consumption"`
	Evaluation  coreevidence.Result         `json:"evaluation"`
	Snapshot    codereview.Snapshot         `json:"-"`
	Target      coreevidence.NativeTarget   `json:"-"`
	Native      nativeEvidenceProvider      `json:"-"`
}

type runnerEvidencePreGate struct {
	app     *app
	profile auth.Profile
}

func newRunnerEvidencePreGate(profile auth.Profile) jobs.EvidencePreGate {
	return &runnerEvidencePreGate{app: newApp(strings.NewReader(""), io.Discard, io.Discard), profile: profile}
}

func (g *runnerEvidencePreGate) BeforeDispatch(ctx context.Context, request jobs.EvidencePreGateRequest) (jobs.EvidencePreGateResult, error) {
	plan, err := workflow.Resolve(request.WorkflowRoot)
	if err != nil {
		return jobs.EvidencePreGateResult{}, fmt.Errorf("resolve runner evidence policy: %w", err)
	}
	if plan.Config.ExternalCode == nil || !plan.Config.ExternalCode.Evidence.SynchronizesBefore("runner") {
		return jobs.EvidencePreGateResult{Skipped: true}, nil
	}
	token, err := readEvidenceCredential(request.CredentialFile)
	if err != nil {
		return jobs.EvidencePreGateResult{}, err
	}
	result, selfHosted, err := g.app.externalGateWithProfile(ctx, g.profile, token, request.Repo, request.IssueNumber,
		"code_change", "", coreevidence.GateVerify, request.WorkflowRoot, "runner")
	if err != nil {
		return jobs.EvidencePreGateResult{}, err
	}
	if !selfHosted {
		return jobs.EvidencePreGateResult{}, errors.New("runner evidence pre-gate requires a self-hosted profile")
	}
	return jobs.EvidencePreGateResult{ProviderKey: result.Consumption.ProviderKey,
		ExternalRepository: result.Consumption.ExternalRepository, ChangeID: result.Consumption.ChangeID,
		SubjectRevision: result.Consumption.SubjectRevision,
		EvidenceIDs:     append([]string(nil), result.Consumption.EvidenceIDs...)}, nil
}

func readEvidenceCredential(path string) (string, error) {
	path = strings.TrimSpace(path)
	info, err := os.Lstat(path)
	if err != nil || path == "" || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 ||
		info.Size() <= 0 || info.Size() > 1<<20 {
		return "", errors.New("runner evidence credential must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("runner evidence credential is unavailable")
	}
	defer clear(raw)
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("runner evidence credential is invalid")
	}
	return token, nil
}

func (a *app) externalGate(ctx context.Context, host, token, repo string, issue int, relationKind,
	expectedRevision string, gate coreevidence.Gate) (externalGateResult, bool, error) {
	return a.externalGateAtRoot(ctx, host, token, repo, issue, relationKind, expectedRevision, gate, ".", string(gate))
}

func (a *app) externalGateAtRoot(ctx context.Context, host, token, repo string, issue int, relationKind,
	expectedRevision string, gate coreevidence.Gate, workflowRoot, syncStage string) (externalGateResult, bool, error) {
	profile, _, err := auth.ResolveProfile(a.profileName, host)
	if err != nil {
		return externalGateResult{}, false, err
	}
	return a.externalGateWithProfile(ctx, profile, token, repo, issue, relationKind, expectedRevision, gate, workflowRoot, syncStage)
}

func (a *app) externalGateWithProfile(ctx context.Context, profile auth.Profile, token, repo string, issue int, relationKind,
	expectedRevision string, gate coreevidence.Gate, workflowRoot, syncStage string) (externalGateResult, bool, error) {
	if profile.Kind != auth.ProfileKindHosted {
		return externalGateResult{}, false, nil
	}
	if a.newNativeEvidenceProvider == nil {
		return externalGateResult{}, true, errors.New("self-hosted native evidence provider is unavailable")
	}
	native, err := a.newNativeEvidenceProvider(profile, token)
	if err != nil {
		return externalGateResult{}, true, err
	}
	target, err := native.ResolveTarget(ctx, repo, issue, relationKind)
	if err != nil {
		return externalGateResult{}, true, err
	}
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision != "" && expectedRevision != target.SubjectRevision {
		return externalGateResult{}, true, fmt.Errorf("external %s revision mismatch: reference is %s, command requested %s",
			relationKind, target.SubjectRevision, expectedRevision)
	}
	plan, err := workflow.Resolve(workflowRoot)
	if err != nil {
		return externalGateResult{}, true, fmt.Errorf("resolve workflow evidence policy: %w", err)
	}
	if config := plan.Config.ExternalCode; config != nil && strings.TrimSpace(config.ProviderKey) != target.Reference.ProviderKey {
		return externalGateResult{}, true, fmt.Errorf("external code provider mismatch: workflow selects %s, active reference uses %s",
			config.ProviderKey, target.Reference.ProviderKey)
	}
	policy, err := mergedEvidencePolicy(plan.Config.ExternalCode, target.Policy)
	if err != nil {
		return externalGateResult{}, true, err
	}
	request := codereview.SnapshotRequest{Reference: target.Reference, SubjectRevision: target.SubjectRevision}
	if plan.Config.ExternalCode != nil && plan.Config.ExternalCode.Evidence.SynchronizesBefore(syncStage) {
		provider, err := a.resolveOperatorEvidenceProvider(ctx, profile, target.Reference.ProviderKey)
		if err != nil {
			return externalGateResult{}, true, fmt.Errorf("resolve operator evidence provider: %w", err)
		}
		providerSnapshot, err := codereview.FetchSnapshot(ctx, provider, request)
		if err != nil {
			return externalGateResult{}, true, fmt.Errorf("fetch external provider facts: %w", err)
		}
		if err := codereview.ValidateProviderSnapshot(providerSnapshot); err != nil {
			return externalGateResult{}, true, fmt.Errorf("validate external provider facts: %w", err)
		}
		if err := native.SynchronizeSnapshot(ctx, target, providerSnapshot); err != nil {
			return externalGateResult{}, true, fmt.Errorf("persist external provider facts: %w", err)
		}
	}
	snapshot, err := codereview.FetchSnapshot(ctx, target.Provider, request)
	if err != nil {
		return externalGateResult{}, true, fmt.Errorf("reload authoritative external evidence ledger: %w", err)
	}
	evaluation := coreevidence.Evaluate(snapshot, policy, coreevidence.Target{Gate: gate,
		Reference: target.Reference, SubjectRevision: target.SubjectRevision, Now: time.Now().UTC()})
	result := externalGateResult{Evaluation: evaluation, Snapshot: snapshot, Target: target, Native: native,
		Consumption: externalEvidenceConsumption{ProviderKey: target.Reference.ProviderKey,
			ExternalRepository: target.Reference.ExternalRepository, ChangeID: target.Reference.ChangeID,
			ReferenceVersion: target.ReferenceVersion, SubjectRevision: target.SubjectRevision,
			EvidenceIDs: append([]string(nil), evaluation.EvidenceIDs...)}}
	if !evaluation.Passed {
		return result, true, externalGateFailure(relationKind, evaluation)
	}
	bindings, bindingErr := authoritativeExternalEvidenceBindings(snapshot, result.Consumption)
	if bindingErr != nil && (gate == coreevidence.GateReview || gate == coreevidence.GateVerify) {
		return result, true, fmt.Errorf("bind authoritative external evidence: %w", bindingErr)
	}
	if bindingErr == nil {
		result.Consumption.Bindings = bindings
	}
	return result, true, nil
}

// authoritativeExternalEvidenceBindings derives PROCESS carrier identity only
// from native-ledger Records selected by the successful evaluation. Provider
// Facts are deliberately ignored because they have not crossed the native
// evidence authority boundary yet.
func authoritativeExternalEvidenceBindings(snapshot codereview.Snapshot, consumption externalEvidenceConsumption) ([]externalEvidenceBinding, error) {
	selected := make(map[string]bool, len(consumption.EvidenceIDs))
	for _, id := range consumption.EvidenceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("selected evidence id is empty")
		}
		selected[id] = true
	}
	if len(selected) == 0 {
		return nil, errors.New("no selected evidence ids")
	}
	records := make(map[string]codereview.EvidenceRecord, len(snapshot.Records))
	for _, record := range snapshot.Records {
		id := strings.TrimSpace(record.ID)
		if !selected[id] {
			continue
		}
		if _, duplicate := records[id]; duplicate {
			return nil, fmt.Errorf("selected evidence id %q is ambiguous", id)
		}
		records[id] = record
	}
	for id := range selected {
		if _, ok := records[id]; !ok {
			return nil, fmt.Errorf("selected evidence id %q is absent from the authoritative ledger", id)
		}
	}

	bindings := make([]externalEvidenceBinding, 0, len(selected))
	for id := range selected {
		record := records[id]
		if record.Kind != codereview.EvidenceReview && record.Kind != codereview.EvidenceCheck {
			continue
		}
		if !record.Trusted || strings.TrimSpace(record.WriterIdentity) == "" {
			return nil, fmt.Errorf("selected %s evidence %q is not trusted authoritative evidence", record.Kind, id)
		}
		revision := strings.TrimSpace(record.SubjectRevision)
		if revision == "" || revision != strings.TrimSpace(consumption.SubjectRevision) {
			return nil, fmt.Errorf("selected %s evidence %q revision does not match consumption", record.Kind, id)
		}
		processID, specID := strings.TrimSpace(record.ProcessID), strings.TrimSpace(record.SpecID)
		if processID == "" && specID == "" && record.Kind == codereview.EvidenceCheck {
			// Current provider-neutral check records intentionally have no workflow
			// linkage. They remain selected gate evidence but cannot independently
			// vouch for a PROCESS carrier.
			continue
		}
		if !externalProcessIDPattern.MatchString(processID) || !externalSpecIDPattern.MatchString(specID) {
			return nil, fmt.Errorf("selected %s evidence %q lacks canonical PROCESS/SPEC linkage", record.Kind, id)
		}
		bindings = append(bindings, externalEvidenceBinding{ProcessID: processID, SpecID: specID, EvidenceID: id,
			Kind: record.Kind, SubjectRevision: revision, Trusted: true, Source: "native-authoritative-ledger"})
	}
	bindings = normalizeExternalEvidenceBindings(bindings)
	if len(bindings) == 0 {
		return nil, errors.New("selected evidence has no authoritative PROCESS/SPEC carrier binding")
	}
	return bindings, nil
}

func normalizeExternalEvidenceBindings(bindings []externalEvidenceBinding) []externalEvidenceBinding {
	sort.SliceStable(bindings, func(i, j int) bool {
		left, right := bindings[i], bindings[j]
		return left.ProcessID+"\x00"+left.SpecID+"\x00"+left.EvidenceID+"\x00"+string(left.Kind) <
			right.ProcessID+"\x00"+right.SpecID+"\x00"+right.EvidenceID+"\x00"+string(right.Kind)
	})
	result := make([]externalEvidenceBinding, 0, len(bindings))
	for _, binding := range bindings {
		if len(result) > 0 && result[len(result)-1] == binding {
			continue
		}
		result = append(result, binding)
	}
	return result
}

func (a *app) resolveOperatorEvidenceProvider(ctx context.Context, profile auth.Profile, key string) (codereview.Provider, error) {
	registry, _, registryErr := codereview.LoadOperatorRegistry(profile.OperatorRegistryFile)
	if registryErr != nil {
		return nil, registryErr
	}
	if provider, err := registry.Lookup(key); err == nil {
		return provider, nil
	}
	// Preserve the app-level seam for hermetic command tests and older callers,
	// while production resolves the full Provider contract directly rather than
	// requiring an adapter to implement unrelated mutation capabilities.
	if a.resolveCodeMutationProvider != nil {
		provider, err := a.resolveCodeMutationProvider(ctx, key)
		if err == nil {
			return provider, nil
		}
		registryErr = err
	}
	if registryErr == nil {
		registryErr = codereview.ErrProviderNotFound
	}
	return nil, registryErr
}

func (c *commandNativeEvidenceClient) SynchronizeSnapshot(ctx context.Context, target coreevidence.NativeTarget,
	snapshot codereview.Snapshot) error {
	if c == nil || c.api == nil || target.OrgID == uuid.Nil || target.RepoID == uuid.Nil || target.IssueID == uuid.Nil ||
		snapshot.Reference != target.Reference || snapshot.SubjectRevision != target.SubjectRevision {
		return fmt.Errorf("%w: snapshot synchronization identity is incomplete", coreevidence.ErrNativeEvidence)
	}
	if err := codereview.ValidateProviderSnapshot(snapshot); err != nil {
		return fmt.Errorf("%w: %v", coreevidence.ErrNativeEvidence, err)
	}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/references", target.OrgID, target.RepoID, target.IssueID)
	var envelope struct {
		References []evidenceSyncReference `json:"references"`
	}
	if err := c.request(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return err
	}
	matches := make([]evidenceSyncReference, 0, 1)
	for _, reference := range envelope.References {
		if reference.IssueID == target.IssueID && reference.ProviderKey == target.Reference.ProviderKey &&
			reference.RelationKind == "code_change" && reference.ExternalRepositoryID == target.Reference.ExternalRepository &&
			reference.ExternalID == target.Reference.ChangeID && reference.LifecycleState == "active" {
			metadata, err := reference.identity()
			if err != nil {
				return err
			}
			if metadata.HeadRevision == target.SubjectRevision {
				matches = append(matches, reference)
			}
		}
	}
	if len(matches) != 1 || matches[0].ID == uuid.Nil || matches[0].RepresentationVersion <= 0 {
		return fmt.Errorf("%w: exact active code_change reference moved or is ambiguous", coreevidence.ErrNativeEvidence)
	}
	reference := matches[0]
	var result struct {
		ReferenceID      uuid.UUID         `json:"reference_id"`
		ReferenceVersion int64             `json:"reference_version"`
		SubjectRevision  string            `json:"subject_revision"`
		Evidence         []json.RawMessage `json:"evidence"`
		Created          int               `json:"created"`
		Replayed         int               `json:"replayed"`
	}
	path = fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/evidence/snapshots", target.OrgID, target.RepoID, target.IssueID)
	body := map[string]any{"reference_id": reference.ID, "expected_reference_version": reference.RepresentationVersion,
		"snapshot": snapshot}
	if err := c.request(ctx, http.MethodPost, path, body, &result); err != nil {
		return err
	}
	if result.ReferenceID != reference.ID || result.ReferenceVersion != reference.RepresentationVersion ||
		result.SubjectRevision != target.SubjectRevision || result.Created < 0 || result.Replayed < 0 ||
		result.Created+result.Replayed != len(snapshot.Facts) || len(result.Evidence) != len(snapshot.Facts) {
		return fmt.Errorf("%w: snapshot persistence response identity mismatch", coreevidence.ErrNativeEvidence)
	}
	return nil
}

type evidenceSyncReference struct {
	ID                    uuid.UUID       `json:"id"`
	IssueID               uuid.UUID       `json:"issue_id"`
	ProviderKey           string          `json:"provider_key"`
	RelationKind          string          `json:"relation_kind"`
	ExternalRepositoryID  string          `json:"external_repository_id"`
	ExternalID            string          `json:"external_id"`
	CanonicalURL          string          `json:"canonical_url"`
	Title                 *string         `json:"title,omitempty"`
	LifecycleState        string          `json:"lifecycle_state"`
	Visibility            string          `json:"visibility"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
	RepresentationVersion int64           `json:"representation_version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

func (r evidenceSyncReference) identity() (struct {
	HeadRevision string `json:"head_revision"`
	BaseRevision string `json:"base_revision,omitempty"`
}, error) {
	var result struct {
		HeadRevision string `json:"head_revision"`
		BaseRevision string `json:"base_revision,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(r.Metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || strings.TrimSpace(result.HeadRevision) == "" {
		return result, fmt.Errorf("%w: invalid code_change reference metadata", coreevidence.ErrNativeEvidence)
	}
	result.HeadRevision = strings.TrimSpace(result.HeadRevision)
	result.BaseRevision = strings.TrimSpace(result.BaseRevision)
	return result, nil
}

func (c *commandNativeEvidenceClient) request(ctx context.Context, method, path string, body, target any) error {
	base, err := url.Parse(c.api.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("%w: invalid native evidence origin", coreevidence.ErrNativeEvidence)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(path, "/")
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, base.String(), reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.api.Token)
	request.Header.Set("X-Request-ID", uuid.NewString())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.api.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: native evidence request failed", coreevidence.ErrNativeEvidence)
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, nativeEvidenceSyncResponseLimit+1))
	if readErr != nil || int64(len(raw)) > nativeEvidenceSyncResponseLimit {
		return fmt.Errorf("%w: native evidence response exceeds 4 MiB", coreevidence.ErrNativeEvidence)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: native evidence request returned HTTP %d (request_id %s)", coreevidence.ErrNativeEvidence,
			response.StatusCode, response.Header.Get("X-Request-ID"))
	}
	if !evidenceSyncNoStore(response.Header.Get("Cache-Control")) {
		return fmt.Errorf("%w: native evidence response is not marked no-store", coreevidence.ErrNativeEvidence)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("%w: native evidence response content type is not application/json", coreevidence.ErrNativeEvidence)
	}
	if err := rejectDuplicateEvidenceSyncJSON(raw); err != nil {
		return fmt.Errorf("%w: %v", coreevidence.ErrNativeEvidence, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: decode native evidence response: %v", coreevidence.ErrNativeEvidence, err)
	}
	return nil
}

func evidenceSyncNoStore(value string) bool {
	for _, directive := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
			return true
		}
	}
	return false
}

func rejectDuplicateEvidenceSyncJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeEvidenceSyncJSON(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("native evidence response has trailing JSON")
	}
	return nil
}

func consumeEvidenceSyncJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate or invalid JSON key %q", key)
			}
			seen[key] = true
			if err := consumeEvidenceSyncJSON(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeEvidenceSyncJSON(decoder); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token()
	return err
}

func (a *app) externalMutationTarget(ctx context.Context, host, token, repo string, issue int,
	relationKind, expectedRevision string, capability codereview.Capability) (coreevidence.NativeTarget, codereview.MutationProvider, nativeEvidenceProvider, bool, error) {
	profile, _, err := auth.ResolveProfile(a.profileName, host)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, false, err
	}
	if profile.Kind != auth.ProfileKindHosted {
		return coreevidence.NativeTarget{}, nil, nil, false, nil
	}
	native, err := a.newNativeEvidenceProvider(profile, token)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	target, err := native.ResolveTarget(ctx, repo, issue, relationKind)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	if expected := strings.TrimSpace(expectedRevision); expected != "" && expected != target.SubjectRevision {
		return coreevidence.NativeTarget{}, nil, nil, true, fmt.Errorf("external %s revision mismatch: reference is %s, command requested %s",
			relationKind, target.SubjectRevision, expected)
	}
	plan, err := workflow.Resolve(".")
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	if plan.Config.ExternalCode != nil && plan.Config.ExternalCode.ProviderKey != target.Reference.ProviderKey {
		return coreevidence.NativeTarget{}, nil, nil, true, fmt.Errorf("external code provider mismatch: workflow selects %s, active reference uses %s",
			plan.Config.ExternalCode.ProviderKey, target.Reference.ProviderKey)
	}
	if a.resolveCodeMutationProvider == nil {
		return coreevidence.NativeTarget{}, nil, nil, true, codereview.ErrProviderNotFound
	}
	provider, err := a.resolveCodeMutationProvider(ctx, target.Reference.ProviderKey)
	if err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	if _, err := codereview.RequireCapabilities(ctx, provider, capability); err != nil {
		return coreevidence.NativeTarget{}, nil, nil, true, err
	}
	return target, provider, native, true, nil
}

func mergedEvidencePolicy(config *workflow.ExternalCodeConfig, native coreevidence.NativePolicy) (coreevidence.Policy, error) {
	policy := coreevidence.Policy{Freshness: map[codereview.EvidenceKind]time.Duration{},
		BlockingReviewSeverities: []string{"P0", "P1"}}
	for _, requirement := range native.Requirements {
		policy.RequiredKinds = append(policy.RequiredKinds, requirement.Kind)
		if requirement.Freshness > 0 {
			policy.Freshness[requirement.Kind] = requirement.Freshness
		}
	}
	if config != nil {
		for _, raw := range config.Evidence.Required {
			kind := codereview.EvidenceKind(strings.TrimSpace(raw))
			if !commandEvidenceKind(kind) {
				return coreevidence.Policy{}, fmt.Errorf("workflow requires unsupported evidence kind %q", raw)
			}
			policy.RequiredKinds = append(policy.RequiredKinds, kind)
		}
		policy.RequiredChecks = append(policy.RequiredChecks, config.Evidence.RequiredChecks...)
		for rawKind, rawDuration := range config.Evidence.Freshness {
			kind := codereview.EvidenceKind(strings.TrimSpace(rawKind))
			duration, err := time.ParseDuration(rawDuration)
			if !commandEvidenceKind(kind) || err != nil || duration <= 0 {
				return coreevidence.Policy{}, fmt.Errorf("workflow evidence freshness %s is invalid", rawKind)
			}
			if current := policy.Freshness[kind]; current == 0 || duration < current {
				policy.Freshness[kind] = duration
			}
		}
	}
	policy.RequiredKinds = dedupeEvidenceKinds(policy.RequiredKinds)
	policy.RequiredChecks = dedupeTrimmed(policy.RequiredChecks)
	return policy, nil
}

func externalGateFailure(relation string, result coreevidence.Result) error {
	parts := make([]string, 0, len(result.Failures))
	for _, failure := range result.Failures {
		message := failure.Code + ": " + failure.Message
		if failure.EvidenceID != "" {
			message += " [" + failure.EvidenceID + "]"
		}
		parts = append(parts, message)
	}
	return fmt.Errorf("external %s evidence gate failed for revision %s: %s", relation,
		result.SubjectRevision, strings.Join(parts, "; "))
}

func commandEvidenceKind(kind codereview.EvidenceKind) bool {
	switch kind {
	case codereview.EvidenceChange, codereview.EvidenceReview, codereview.EvidenceCheck,
		codereview.EvidenceMerge, codereview.EvidenceArchive:
		return true
	default:
		return false
	}
}

func dedupeEvidenceKinds(values []codereview.EvidenceKind) []codereview.EvidenceKind {
	seen := map[codereview.EvidenceKind]bool{}
	result := make([]codereview.EvidenceKind, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func dedupeTrimmed(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
