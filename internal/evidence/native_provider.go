package evidence

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
)

const nativeResponseLimit = int64(4 << 20)
const nativeRequestTimeout = 30 * time.Second

var ErrNativeEvidence = errors.New("native evidence provider rejected the server response")

type NativeProvider struct {
	profile auth.Profile
	token   string
	client  *http.Client
	now     func() time.Time
}

type NativeTarget struct {
	Reference       codereview.Reference
	SubjectRevision string
	BaseRevision    string
	Policy          NativePolicy
	Provider        codereview.Provider
	CanonicalURL    string
	IssueID         uuid.UUID
	OrgID           uuid.UUID
	RepoID          uuid.UUID
}

type NativePolicy struct {
	Requirements []NativeRequirement
}

type NativeRequirement struct {
	Kind      codereview.EvidenceKind
	Freshness time.Duration
}

func NewNativeProvider(profile auth.Profile, token string) (*NativeProvider, error) {
	profile, err := profile.Normalized()
	if err != nil {
		return nil, err
	}
	if profile.Kind != auth.ProfileKindHosted || strings.TrimSpace(token) == "" {
		return nil, errors.New("native evidence provider requires a self-hosted profile and realm token")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	if profile.CAFile != "" {
		pem, err := os.ReadFile(profile.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read profile CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("profile CA contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	return &NativeProvider{profile: profile, token: token, now: func() time.Time { return time.Now().UTC() }, client: &http.Client{
		Transport: transport, Timeout: nativeRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("native evidence redirects are forbidden")
		},
	}}, nil
}

func (p *NativeProvider) ResolveTarget(ctx context.Context, repo string, issueNumber int, relationKind string) (NativeTarget, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") || issueNumber <= 0 {
		return NativeTarget{}, fmt.Errorf("%w: invalid repository or issue", ErrNativeEvidence)
	}
	orgID, err := p.resolveOrganization(ctx, owner)
	if err != nil {
		return NativeTarget{}, err
	}
	repoID, err := p.resolveRepository(ctx, orgID, name)
	if err != nil {
		return NativeTarget{}, err
	}
	issueID, err := p.resolveIssueID(ctx, owner, name, issueNumber)
	if err != nil {
		return NativeTarget{}, err
	}
	policy, err := p.loadPolicy(ctx, orgID, repoID)
	if err != nil {
		return NativeTarget{}, err
	}
	references, err := p.loadReferences(ctx, orgID, repoID, issueID)
	if err != nil {
		return NativeTarget{}, err
	}
	matching := make([]nativeReference, 0, 1)
	for _, reference := range references {
		if reference.RelationKind == relationKind && reference.LifecycleState == "active" {
			matching = append(matching, reference)
		}
	}
	if len(matching) != 1 {
		return NativeTarget{}, fmt.Errorf("%w: issue must have exactly one active %s reference (found %d)", ErrNativeEvidence, relationKind, len(matching))
	}
	metadata, err := decodeReferenceMetadata(matching[0].Metadata)
	if err != nil {
		return NativeTarget{}, err
	}
	reference := codereview.Reference{ProviderKey: matching[0].ProviderKey,
		ExternalRepository: matching[0].ExternalRepositoryID, ChangeID: matching[0].ExternalID}
	if err := reference.Validate(); err != nil {
		return NativeTarget{}, fmt.Errorf("%w: %v", ErrNativeEvidence, err)
	}
	bound := &nativeSnapshotProvider{parent: p, orgID: orgID, repoID: repoID, issueID: issueID,
		reference: reference, canonicalURL: matching[0].CanonicalURL}
	return NativeTarget{Reference: reference, SubjectRevision: metadata.HeadRevision, BaseRevision: metadata.BaseRevision,
		Policy: policy, Provider: bound, CanonicalURL: matching[0].CanonicalURL, IssueID: issueID, OrgID: orgID, RepoID: repoID}, nil
}

func (p *NativeProvider) UpsertArchiveReference(ctx context.Context, target NativeTarget, reference codereview.Reference, canonicalURL, headRevision, baseRevision string) error {
	if reference.ProviderKey != target.Reference.ProviderKey || reference.ExternalRepository != target.Reference.ExternalRepository ||
		strings.TrimSpace(reference.ChangeID) == "" || strings.TrimSpace(headRevision) == "" {
		return fmt.Errorf("%w: archive reference identity mismatch", ErrNativeEvidence)
	}
	existing, err := p.loadReferences(ctx, target.OrgID, target.RepoID, target.IssueID)
	if err != nil {
		return err
	}
	active := make([]nativeReference, 0, 1)
	for _, candidate := range existing {
		if candidate.RelationKind == "archive_change" && candidate.LifecycleState == "active" {
			active = append(active, candidate)
		}
	}
	if len(active) > 1 {
		return fmt.Errorf("%w: issue already has multiple active archive_change references", ErrNativeEvidence)
	}
	if len(active) == 1 {
		metadata, metadataErr := decodeReferenceMetadata(active[0].Metadata)
		if metadataErr != nil || active[0].ProviderKey != reference.ProviderKey ||
			active[0].ExternalRepositoryID != reference.ExternalRepository || active[0].ExternalID != reference.ChangeID ||
			metadata.HeadRevision != strings.TrimSpace(headRevision) || metadata.BaseRevision != strings.TrimSpace(baseRevision) {
			return fmt.Errorf("%w: a different active archive_change reference already exists", ErrNativeEvidence)
		}
		return nil
	}
	body := map[string]any{"issue_id": target.IssueID, "provider_key": reference.ProviderKey,
		"relation_kind": "archive_change", "external_repository_id": reference.ExternalRepository,
		"external_id": reference.ChangeID, "canonical_url": canonicalURL, "lifecycle_state": "active",
		"visibility": "repository", "metadata": map[string]string{"head_revision": strings.TrimSpace(headRevision), "base_revision": strings.TrimSpace(baseRevision)}}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/references", target.OrgID, target.RepoID, target.IssueID)
	var result nativeReference
	if err := p.request(ctx, http.MethodPut, p.profile.NativeAPIURL, path, nil, body, &result); err != nil {
		return err
	}
	if result.RelationKind != "archive_change" || result.ProviderKey != reference.ProviderKey ||
		result.ExternalRepositoryID != reference.ExternalRepository || result.ExternalID != reference.ChangeID {
		return fmt.Errorf("%w: archive reference response identity mismatch", ErrNativeEvidence)
	}
	return nil
}

type nativeSnapshotProvider struct {
	parent       *NativeProvider
	orgID        uuid.UUID
	repoID       uuid.UUID
	issueID      uuid.UUID
	reference    codereview.Reference
	canonicalURL string
}

func (p *nativeSnapshotProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
		Values: []codereview.Capability{codereview.CapabilityEvidenceSnapshot}}, nil
}

func (p *nativeSnapshotProvider) Snapshot(ctx context.Context, request codereview.SnapshotRequest) (codereview.Snapshot, error) {
	if request.Reference != p.reference || strings.TrimSpace(request.SubjectRevision) == "" {
		return codereview.Snapshot{}, fmt.Errorf("%w: snapshot request identity mismatch", ErrNativeEvidence)
	}
	query := url.Values{"provider_key": {request.Reference.ProviderKey},
		"external_repository_id": {request.Reference.ExternalRepository}, "subject_revision": {request.SubjectRevision}}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/evidence", p.orgID, p.repoID, p.issueID)
	var envelope struct {
		Evidence []nativeEvidence `json:"evidence"`
	}
	if err := p.parent.request(ctx, http.MethodGet, p.parent.profile.NativeAPIURL, path, query, nil, &envelope); err != nil {
		return codereview.Snapshot{}, err
	}
	records := make([]codereview.EvidenceRecord, 0, len(envelope.Evidence))
	for _, item := range envelope.Evidence {
		record, err := item.record(request.Reference.ChangeID)
		if err != nil {
			return codereview.Snapshot{}, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: request.Reference,
		SubjectRevision: request.SubjectRevision, Records: records, CapturedAt: p.parent.now()}, nil
}

func (p *NativeProvider) resolveOrganization(ctx context.Context, owner string) (uuid.UUID, error) {
	var envelope struct {
		User           json.RawMessage    `json:"user"`
		Credential     json.RawMessage    `json:"credential"`
		Session        json.RawMessage    `json:"session,omitempty"`
		AllowedActions []string           `json:"allowed_actions"`
		Organizations  []nativeOrgContext `json:"organizations"`
	}
	if err := p.request(ctx, http.MethodGet, p.profile.NativeAPIURL, "/context", nil, nil, &envelope); err != nil {
		return uuid.Nil, err
	}
	matches := make([]uuid.UUID, 0, 1)
	for _, org := range envelope.Organizations {
		if org.Name == owner {
			matches = append(matches, org.ID)
		}
	}
	if len(matches) != 1 {
		return uuid.Nil, fmt.Errorf("%w: repository owner is unavailable or ambiguous", ErrNativeEvidence)
	}
	return matches[0], nil
}

func (p *NativeProvider) resolveRepository(ctx context.Context, orgID uuid.UUID, name string) (uuid.UUID, error) {
	var envelope struct {
		Repositories []nativeRepoContext `json:"repositories"`
	}
	if err := p.request(ctx, http.MethodGet, p.profile.NativeAPIURL, "/context/orgs/"+orgID.String()+"/repos", nil, nil, &envelope); err != nil {
		return uuid.Nil, err
	}
	matches := make([]uuid.UUID, 0, 1)
	for _, item := range envelope.Repositories {
		if item.Repository.Name == name && item.Repository.OrganizationID == orgID {
			matches = append(matches, item.Repository.ID)
		}
	}
	if len(matches) != 1 {
		return uuid.Nil, fmt.Errorf("%w: repository is unavailable or ambiguous", ErrNativeEvidence)
	}
	return matches[0], nil
}

func (p *NativeProvider) resolveIssueID(ctx context.Context, owner, repo string, number int) (uuid.UUID, error) {
	var item nativeIssueIdentity
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	if err := p.request(ctx, http.MethodGet, p.profile.APIURL, path, nil, nil, &item); err != nil {
		return uuid.Nil, err
	}
	decoded, err := base64.RawStdEncoding.DecodeString(item.NodeID)
	if err != nil || !strings.HasPrefix(string(decoded), "Issue:") {
		return uuid.Nil, fmt.Errorf("%w: invalid self-hosted issue node_id", ErrNativeEvidence)
	}
	id, err := uuid.Parse(strings.TrimPrefix(string(decoded), "Issue:"))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: invalid self-hosted issue node_id", ErrNativeEvidence)
	}
	return id, nil
}

func (p *NativeProvider) loadReferences(ctx context.Context, orgID, repoID, issueID uuid.UUID) ([]nativeReference, error) {
	var envelope struct {
		References []nativeReference `json:"references"`
	}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/references", orgID, repoID, issueID)
	if err := p.request(ctx, http.MethodGet, p.profile.NativeAPIURL, path, nil, nil, &envelope); err != nil {
		return nil, err
	}
	return envelope.References, nil
}

func (p *NativeProvider) loadPolicy(ctx context.Context, orgID, repoID uuid.UUID) (NativePolicy, error) {
	var item nativePolicy
	path := fmt.Sprintf("/orgs/%s/repos/%s/evidence/policy", orgID, repoID)
	if err := p.request(ctx, http.MethodGet, p.profile.NativeAPIURL, path, nil, nil, &item); err != nil {
		return NativePolicy{}, err
	}
	result := NativePolicy{Requirements: make([]NativeRequirement, 0, len(item.Requirements))}
	for _, requirement := range item.Requirements {
		kind := codereview.EvidenceKind(strings.TrimSpace(requirement.EvidenceType))
		if !validEvidenceKind(kind) || requirement.Freshness < 0 {
			return NativePolicy{}, fmt.Errorf("%w: invalid repository evidence policy", ErrNativeEvidence)
		}
		result.Requirements = append(result.Requirements, NativeRequirement{Kind: kind, Freshness: requirement.Freshness})
	}
	return result, nil
}

func (p *NativeProvider) request(ctx context.Context, method, base, path string, query url.Values, body any, target any) error {
	endpoint, err := nativeURL(base, path, query)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("X-Request-ID", uuid.NewString())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: native request failed", ErrNativeEvidence)
	}
	defer response.Body.Close()
	if !hasNoStore(response.Header.Get("Cache-Control")) {
		return fmt.Errorf("%w: native response is not marked no-store", ErrNativeEvidence)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, nativeResponseLimit+1))
	if err != nil || int64(len(raw)) > nativeResponseLimit {
		return fmt.Errorf("%w: native response exceeds 4 MiB", ErrNativeEvidence)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: native request returned HTTP %d (request_id %s)", ErrNativeEvidence,
			response.StatusCode, response.Header.Get("X-Request-ID"))
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("%w: native response content type is not application/json", ErrNativeEvidence)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrNativeEvidence, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: decode native response: %v", ErrNativeEvidence, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: native response has trailing JSON", ErrNativeEvidence)
	}
	return nil
}

func nativeURL(base, path string, query url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: invalid native origin", ErrNativeEvidence)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawPath = ""
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func hasNoStore(value string) bool {
	for _, directive := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
			return true
		}
	}
	return false
}

type nativeOrgContext struct {
	ID                  uuid.UUID `json:"id"`
	Name                string    `json:"name"`
	DisplayName         string    `json:"display_name"`
	EffectivePermission string    `json:"effective_permission"`
	ContainerOnly       bool      `json:"container_only"`
	AllowedActions      []string  `json:"allowed_actions"`
}

type nativeRepoContext struct {
	Repository struct {
		ID                 uuid.UUID `json:"id"`
		OrganizationID     uuid.UUID `json:"organization_id"`
		Name               string    `json:"name"`
		DisplayName        string    `json:"display_name"`
		Visibility         string    `json:"visibility"`
		ContributionPolicy string    `json:"contribution_policy"`
	} `json:"repository"`
	EffectivePermission string   `json:"effective_permission"`
	AllowedActions      []string `json:"allowed_actions"`
}

type nativeIssueIdentity struct {
	ID, URL, RepositoryURL, LabelsURL, CommentsURL, EventsURL, HTMLURL json.RawMessage `json:"-"`
	NodeID                                                             string          `json:"node_id"`
	Number, State, StateReason, Title, Body, User, Labels, Locked      json.RawMessage `json:"-"`
	Comments, CreatedAt, UpdatedAt, ClosedAt, Reactions                json.RawMessage `json:"-"`
}

func (n *nativeIssueIdentity) UnmarshalJSON(raw []byte) error {
	type wire struct {
		ID            json.RawMessage `json:"id"`
		NodeID        string          `json:"node_id"`
		URL           json.RawMessage `json:"url"`
		RepositoryURL json.RawMessage `json:"repository_url"`
		LabelsURL     json.RawMessage `json:"labels_url"`
		CommentsURL   json.RawMessage `json:"comments_url"`
		EventsURL     json.RawMessage `json:"events_url"`
		HTMLURL       json.RawMessage `json:"html_url"`
		Number        json.RawMessage `json:"number"`
		State         json.RawMessage `json:"state"`
		StateReason   json.RawMessage `json:"state_reason"`
		Title         json.RawMessage `json:"title"`
		Body          json.RawMessage `json:"body"`
		User          json.RawMessage `json:"user"`
		Labels        json.RawMessage `json:"labels"`
		Locked        json.RawMessage `json:"locked"`
		Comments      json.RawMessage `json:"comments"`
		CreatedAt     json.RawMessage `json:"created_at"`
		UpdatedAt     json.RawMessage `json:"updated_at"`
		ClosedAt      json.RawMessage `json:"closed_at"`
		Reactions     json.RawMessage `json:"reactions"`
	}
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	n.NodeID = value.NodeID
	return nil
}

type nativeReference struct {
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

type nativePolicy struct {
	RepresentationVersion int64 `json:"representation_version"`
	Requirements          []struct {
		EvidenceType          string        `json:"evidence_type"`
		Freshness             time.Duration `json:"freshness,omitempty"`
		RepresentationVersion int64         `json:"representation_version"`
	} `json:"requirements"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type referenceMetadata struct {
	HeadRevision string `json:"head_revision"`
	BaseRevision string `json:"base_revision,omitempty"`
}

func decodeReferenceMetadata(raw json.RawMessage) (referenceMetadata, error) {
	var result referenceMetadata
	if len(raw) == 0 || string(raw) == "null" {
		return result, fmt.Errorf("%w: code change reference metadata is required", ErrNativeEvidence)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || strings.TrimSpace(result.HeadRevision) == "" {
		return referenceMetadata{}, fmt.Errorf("%w: code change metadata must contain only head_revision and optional base_revision", ErrNativeEvidence)
	}
	result.HeadRevision = strings.TrimSpace(result.HeadRevision)
	result.BaseRevision = strings.TrimSpace(result.BaseRevision)
	return result, nil
}

type nativeEvidence struct {
	ID                   uuid.UUID       `json:"id"`
	IssueID              uuid.UUID       `json:"issue_id"`
	ProviderKey          string          `json:"provider_key"`
	ExternalRepositoryID string          `json:"external_repository_id"`
	EvidenceType         string          `json:"evidence_type"`
	ExternalID           string          `json:"external_id,omitempty"`
	IngestKey            string          `json:"ingest_key"`
	NormalizedState      string          `json:"normalized_state"`
	SubjectRevision      string          `json:"subject_revision"`
	BaseRevision         *string         `json:"base_revision,omitempty"`
	MergeRevision        *string         `json:"merge_revision,omitempty"`
	ObservedAt           time.Time       `json:"observed_at"`
	ValidUntil           *time.Time      `json:"valid_until,omitempty"`
	PayloadHash          []byte          `json:"payload_hash"`
	Payload              json.RawMessage `json:"payload,omitempty"`
	Provenance           json.RawMessage `json:"provenance,omitempty"`
	WriterUserID         uuid.UUID       `json:"writer_user_id"`
	WriterIdentityKey    string          `json:"writer_identity_key"`
	SupersedesEvidenceID *uuid.UUID      `json:"supersedes_evidence_id,omitempty"`
	Visibility           string          `json:"visibility"`
	CreatedAt            time.Time       `json:"created_at"`
}

type neutralPayloadV1 struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	ChangeID      string          `json:"change_id"`
	Name          string          `json:"name,omitempty"`
	Severity      string          `json:"severity,omitempty"`
	CanonicalURL  string          `json:"canonical_url,omitempty"`
	Summary       string          `json:"summary,omitempty"`
	Path          string          `json:"path,omitempty"`
	Line          *int            `json:"line,omitempty"`
	Approved      json.RawMessage `json:"approved,omitempty"`
}

func (n nativeEvidence) record(changeID string) (codereview.EvidenceRecord, error) {
	kind := codereview.EvidenceKind(strings.TrimSpace(n.EvidenceType))
	if !validEvidenceKind(kind) || n.ID == uuid.Nil || n.IssueID == uuid.Nil || n.ProviderKey == "" ||
		n.ExternalRepositoryID == "" || n.SubjectRevision == "" || n.WriterUserID == uuid.Nil || n.WriterIdentityKey == "" {
		return codereview.EvidenceRecord{}, fmt.Errorf("%w: malformed native evidence identity", ErrNativeEvidence)
	}
	payload := neutralPayloadV1{}
	raw := n.Payload
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return codereview.EvidenceRecord{}, fmt.Errorf("%w: invalid neutral evidence payload: %v", ErrNativeEvidence, err)
	}
	if len(payload.Approved) != 0 || (payload.SchemaVersion != "" && payload.SchemaVersion != "issue-spec.evidence/v1") ||
		strings.TrimSpace(payload.ChangeID) != changeID {
		return codereview.EvidenceRecord{}, fmt.Errorf("%w: evidence payload change identity is missing or contains an untrusted approval", ErrNativeEvidence)
	}
	if kind == codereview.EvidenceCheck && strings.TrimSpace(payload.Name) == "" {
		return codereview.EvidenceRecord{}, fmt.Errorf("%w: check evidence requires a neutral name", ErrNativeEvidence)
	}
	if kind == codereview.EvidenceReview && strings.TrimSpace(payload.Severity) == "" {
		return codereview.EvidenceRecord{}, fmt.Errorf("%w: review evidence requires a neutral severity", ErrNativeEvidence)
	}
	record := codereview.EvidenceRecord{ID: n.ID.String(), Kind: kind, ExternalID: n.ExternalID,
		State: n.NormalizedState, SubjectRevision: n.SubjectRevision, Name: payload.Name, Severity: payload.Severity,
		ObservedAt: n.ObservedAt, ValidUntil: n.ValidUntil, Trusted: true, WriterIdentity: n.WriterIdentityKey,
		CanonicalURL: payload.CanonicalURL, PayloadDigest: hex.EncodeToString(n.PayloadHash)}
	if n.BaseRevision != nil {
		record.BaseRevision = *n.BaseRevision
	}
	if n.MergeRevision != nil {
		record.MergeRevision = *n.MergeRevision
	}
	if n.SupersedesEvidenceID != nil {
		record.SupersedesID = n.SupersedesEvidenceID.String()
	}
	return record, nil
}

func validEvidenceKind(kind codereview.EvidenceKind) bool {
	switch kind {
	case codereview.EvidenceChange, codereview.EvidenceReview, codereview.EvidenceCheck, codereview.EvidenceMerge, codereview.EvidenceArchive:
		return true
	default:
		return false
	}
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing JSON")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
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
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token()
	return err
}
