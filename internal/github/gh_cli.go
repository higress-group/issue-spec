package github

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	ghCLIName        = "gh"
	ghCLIKind        = "external-cli"
	githubDotCom     = "github.com"
	githubAPIVersion = "X-GitHub-Api-Version: 2022-11-28"
)

type GHCLIOptions struct {
	Binary   string
	Runner   ExternalCLIRunner
	Redactor ExternalCLIRedactor
}

type GHCLI struct {
	cli *ExternalCLI
}

func NewGHCLI(options GHCLIOptions) (*GHCLI, error) {
	binary := strings.TrimSpace(options.Binary)
	if binary == "" {
		binary = ghCLIName
	}
	redactor := options.Redactor.WithValues(ghSensitiveEnvTokenValues()...)
	cli, err := NewExternalCLI(ExternalCLIDescriptor{
		Identity:    ExternalCLIIdentity{Name: ghCLIName, Binary: binary},
		HostAdapter: ghHostAdapter{},
		APIAdapter:  GHAPIAdapter{},
	}, options.Runner, redactor)
	if err != nil {
		return nil, err
	}
	return &GHCLI{cli: cli}, nil
}

func GHAuthenticated(ctx context.Context, host string) error {
	cli, err := NewGHCLI(GHCLIOptions{})
	if err != nil {
		return err
	}
	return cli.Authenticated(ctx, host)
}

func GHAuthToken(ctx context.Context, host string) (string, error) {
	cli, err := NewGHCLI(GHCLIOptions{})
	if err != nil {
		return "", err
	}
	return cli.Token(ctx, host)
}

func (g *GHCLI) Authenticated(ctx context.Context, host string) error {
	_, err := g.runAuth(ctx, host, "auth status", []string{"auth", "status", "--active"})
	return err
}

func (g *GHCLI) Token(ctx context.Context, host string) (string, error) {
	result, err := g.runAuth(ctx, host, "auth token", []string{"auth", "token"})
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(result.Stdout))
	if token == "" {
		return "", fmt.Errorf("gh auth token returned empty token for %s", normalizeHost(host))
	}
	return token, nil
}

func (g *GHCLI) RunAPI(ctx context.Context, host string, request ExternalCLIAPIRequest) (ExternalCLIResult, error) {
	if err := request.Validate(); err != nil {
		return ExternalCLIResult{}, err
	}
	command, err := g.cli.Descriptor.APIAdapter.BuildCommand(g.cli.Descriptor.Identity, mustHostArgs(g.cli, host), request)
	if err != nil {
		return ExternalCLIResult{}, err
	}
	command.Operation = request.Operation
	command.Host = normalizeHost(host)
	command.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	command.Endpoint = strings.TrimSpace(request.Endpoint)
	result, runErr := g.cli.Runner.RunCLI(ctx, command)
	if ghResult, ok := parseGHAPIResult(result); ok {
		result = ghResult
	}
	if isGHNoChange(result) {
		return result, nil
	}
	if runErr != nil || result.ExitCode != 0 {
		return result, g.cli.Descriptor.ErrorAdapter.CommandError(g.cli.Descriptor, command, result, runErr, g.cli.Redactor)
	}
	return result, nil
}

func mustHostArgs(cli *ExternalCLI, host string) []string {
	hostArgs, _ := cli.hostArgs(host)
	return hostArgs
}

func isGHNoChange(result ExternalCLIResult) bool {
	if result.ExitCode != 1 || result.Status != http.StatusNotModified {
		return false
	}
	return len(result.Stdout) == 0 && len(result.Stderr) == 0
}

func parseGHAPIResult(result ExternalCLIResult) (ExternalCLIResult, bool) {
	if len(result.Stdout) == 0 {
		return result, false
	}
	headers, body, status, ok := splitGHResponse(result.Stdout)
	if !ok {
		return result, false
	}
	result.Stdout = body
	result.Headers = headers
	result.Status = status
	return result, true
}

func splitGHResponse(data []byte) (http.Header, []byte, int, bool) {
	br := bufio.NewReader(bytes.NewReader(data))
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, nil, 0, false
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "HTTP/") {
		return nil, nil, 0, false
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return nil, nil, 0, false
	}
	status, _ := parseInt(strings.TrimSpace(parts[1]))
	headers := http.Header{}
	for {
		hline, err := br.ReadString('\n')
		if err != nil {
			return nil, nil, 0, false
		}
		hline = strings.TrimRight(hline, "\r\n")
		if hline == "" {
			break
		}
		if idx := strings.IndexByte(hline, ':'); idx > 0 {
			headers.Add(strings.TrimSpace(hline[:idx]), strings.TrimSpace(hline[idx+1:]))
		}
	}
	body, _ := io.ReadAll(br)
	return headers, body, status, true
}

func parseInt(v string) (int, error) {
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid int")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func (g *GHCLI) runAuth(ctx context.Context, host, operation string, args []string) (ExternalCLIResult, error) {
	args = append(append([]string{}, args...), ghHostArgs(host)...)
	command := ExternalCLICommand{
		Binary:    g.cli.Descriptor.Identity.Binary,
		Args:      args,
		Operation: operation,
		Host:      normalizeHost(host),
	}
	return g.cli.run(ctx, command)
}

type ghHostAdapter struct{}

func (ghHostAdapter) HostArgs(host string) ([]string, error) {
	return ghHostArgs(host), nil
}

func ghHostArgs(host string) []string {
	host = normalizeHost(host)
	if host == "" || strings.EqualFold(host, githubDotCom) {
		return nil
	}
	return []string{"--hostname", host}
}

type GHAPIAdapter struct{}

func (GHAPIAdapter) BuildCommand(identity ExternalCLIIdentity, hostArgs []string, request ExternalCLIAPIRequest) (ExternalCLICommand, error) {
	body, err := request.EncodedBody()
	if err != nil {
		return ExternalCLICommand{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	endpoint := endpointWithQuery(strings.TrimSpace(request.Endpoint), request.Query)
	args := []string{"api", "--method", method, "--header", githubAPIVersion}
	args = append(args, hostArgs...)
	if request.Paginate {
		args = append(args, "--paginate")
	}
	if request.Include {
		args = append(args, "--include")
	}
	if body != nil {
		args = append(args, "--input", "-")
	}
	args = append(args, endpoint)
	return ExternalCLICommand{Binary: identity.Binary, Args: args, Stdin: body}, nil
}

func endpointWithQuery(endpoint string, query url.Values) string {
	encoded := query.Encode()
	if encoded == "" {
		return endpoint
	}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + encoded
}

func ghSensitiveEnvTokenValues() []string {
	var values []string
	for _, envName := range []string{"ISSUE_SPEC_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			values = append(values, value)
		}
	}
	return values
}

type GHBackendOptions struct {
	Host       string
	CLIOptions GHCLIOptions
}

type GHBackend struct {
	unsupportedGHOperations

	Host string
	cli  *GHCLI
}

func NewGHBackend(options GHBackendOptions) (*GHBackend, error) {
	cli, err := NewGHCLI(options.CLIOptions)
	if err != nil {
		return nil, err
	}
	return &GHBackend{Host: normalizeHost(options.Host), cli: cli}, nil
}

func (b *GHBackend) BackendInfo() BackendInfo {
	return BackendInfo{Name: ghCLIName, Kind: ghCLIKind, Host: b.Host}
}

type unsupportedGHOperations struct{}

func unsupportedGHOperation(operation string) error {
	return fmt.Errorf("gh backend operation %s is not implemented by runner/auth foundation; PROCESS-004/005 own GitHub API operation families", operation)
}

func (unsupportedGHOperations) GetUser(context.Context) (User, []string, error) {
	return User{}, nil, unsupportedGHOperation("GetUser")
}

func (unsupportedGHOperations) CreateIssue(context.Context, string, string, string, []string) (Issue, error) {
	return Issue{}, unsupportedGHOperation("CreateIssue")
}

func (unsupportedGHOperations) GetIssue(context.Context, string, int) (Issue, error) {
	return Issue{}, unsupportedGHOperation("GetIssue")
}

func (unsupportedGHOperations) UpdateIssue(context.Context, string, int, UpdateIssueOptions) (Issue, error) {
	return Issue{}, unsupportedGHOperation("UpdateIssue")
}

func (unsupportedGHOperations) ListIssueComments(context.Context, string, int) ([]Comment, error) {
	return nil, unsupportedGHOperation("ListIssueComments")
}

func (unsupportedGHOperations) CreateComment(context.Context, string, int, string) (Comment, error) {
	return Comment{}, unsupportedGHOperation("CreateComment")
}

func (unsupportedGHOperations) UpdateComment(context.Context, string, int64, string) (Comment, error) {
	return Comment{}, unsupportedGHOperation("UpdateComment")
}

func (unsupportedGHOperations) CreateLabel(context.Context, string, string, string, string) (LabelResult, error) {
	return LabelResult{}, unsupportedGHOperation("CreateLabel")
}

func (unsupportedGHOperations) GetPullRequest(context.Context, string, int) (PullRequest, error) {
	return PullRequest{}, unsupportedGHOperation("GetPullRequest")
}

func (unsupportedGHOperations) CreatePullRequest(context.Context, string, CreatePullRequestOptions) (PullRequest, error) {
	return PullRequest{}, unsupportedGHOperation("CreatePullRequest")
}

func (unsupportedGHOperations) ListPullRequestFiles(context.Context, string, int) ([]PullRequestFile, error) {
	return nil, unsupportedGHOperation("ListPullRequestFiles")
}

func (unsupportedGHOperations) ListPullRequestReviewComments(context.Context, string, int) ([]PullRequestReviewComment, error) {
	return nil, unsupportedGHOperation("ListPullRequestReviewComments")
}

func (unsupportedGHOperations) CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (PullRequestReviewComment, error) {
	return PullRequestReviewComment{}, unsupportedGHOperation("CreatePullRequestReviewComment")
}

func (unsupportedGHOperations) ReplyPullRequestReviewComment(context.Context, string, int, int64, string) (PullRequestReviewComment, error) {
	return PullRequestReviewComment{}, unsupportedGHOperation("ReplyPullRequestReviewComment")
}

func (unsupportedGHOperations) GetCombinedStatus(context.Context, string, string) (CombinedStatus, error) {
	return CombinedStatus{}, unsupportedGHOperation("GetCombinedStatus")
}

func (unsupportedGHOperations) ListCheckRuns(context.Context, string, string) ([]CheckRun, error) {
	return nil, unsupportedGHOperation("ListCheckRuns")
}

var _ Backend = (*GHBackend)(nil)
