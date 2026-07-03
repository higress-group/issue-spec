package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestExternalCLIFakeAdapterProof(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte(`{"id":7,"title":"created"}`)},
	}
	cli, err := NewExternalCLI(ExternalCLIDescriptor{
		Identity: ExternalCLIIdentity{Name: "fake-glab", Binary: "fake-glab"},
		VersionProbe: ExternalCLIProbe{
			Args: []string{"--version"},
		},
		AuthProbe: ExternalCLIProbe{
			Args: []string{"auth", "status"},
		},
		HostAdapter: HostFlagAdapter{Flag: "--host", DefaultHost: "gitlab.com"},
		APIAdapter:  fakeAPIAdapter{subcommand: []string{"api"}},
	}, runner, ExternalCLIRedactor{})
	if err != nil {
		t.Fatal(err)
	}

	request := ExternalCLIAPIRequest{
		Method:   http.MethodPost,
		Endpoint: "/projects/1/issues",
		Query:    url.Values{"labels": {"bug"}, "page": {"1"}},
		Body:     map[string]string{"title": "created"},
		Paginate: true,
	}
	result, err := cli.RunAPI(context.Background(), "gitlab.example.com", request)
	if err != nil {
		t.Fatal(err)
	}

	wantArgs := []string{
		"api",
		"--host", "gitlab.example.com",
		"--method", http.MethodPost,
		"--query", "labels=bug",
		"--query", "page=1",
		"--paginate",
		"/projects/1/issues",
	}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.command.Args, wantArgs)
	}
	if runner.command.Binary != "fake-glab" {
		t.Fatalf("binary = %q", runner.command.Binary)
	}
	var body map[string]string
	if err := json.Unmarshal(runner.command.Stdin, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "created" {
		t.Fatalf("stdin body = %#v", body)
	}
	var decoded struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	if err := DecodeCLIJSON(result.Stdout, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID != 7 || decoded.Title != "created" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestExternalCLIProbeUsesIdentityHostAndRunner(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte("authenticated")},
	}
	descriptor := ExternalCLIDescriptor{
		Identity:    ExternalCLIIdentity{Name: "fake-cli", Binary: "fake-cli"},
		AuthProbe:   ExternalCLIProbe{Args: []string{"auth", "status"}},
		HostAdapter: HostFlagAdapter{Flag: "--host", DefaultHost: "example.com", OmitDefault: true},
		APIAdapter:  fakeAPIAdapter{subcommand: []string{"api"}},
	}
	cli, err := NewExternalCLI(descriptor, runner, ExternalCLIRedactor{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cli.RunProbe(context.Background(), "enterprise.example.com", descriptor.AuthProbe); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--host", "enterprise.example.com", "auth", "status"}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("probe args = %#v, want %#v", runner.command.Args, wantArgs)
	}

	if _, err := cli.RunProbe(context.Background(), "example.com", descriptor.AuthProbe); err != nil {
		t.Fatal(err)
	}
	wantArgs = []string{"auth", "status"}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("default host probe args = %#v, want %#v", runner.command.Args, wantArgs)
	}
}

func TestExternalCLIErrorRedactsCommandAndStderr(t *testing.T) {
	secret := "ghp_secret"
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stderr: []byte("token " + secret + " rejected"), ExitCode: 2},
	}
	cli, err := NewExternalCLI(ExternalCLIDescriptor{
		Identity:    ExternalCLIIdentity{Name: "fake-cli", Binary: "fake-cli"},
		HostAdapter: HostFlagAdapter{Flag: "--host"},
		APIAdapter:  fakeAPIAdapter{subcommand: []string{"api", secret}},
	}, runner, NewExternalCLIRedactor(secret))
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.RunAPI(context.Background(), "example.com", ExternalCLIAPIRequest{
		Method:   http.MethodGet,
		Endpoint: "/user",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatalf("error leaked secret: %s", message)
	}
	for _, want := range []string{"fake-cli command failed", "exit 2", "[REDACTED]", "rejected"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func TestDecodeCLIJSONPageStreams(t *testing.T) {
	var items []struct {
		ID int `json:"id"`
	}
	if err := DecodeCLIJSONPageStream([]byte(`[{"id":1}]
[{"id":2},{"id":3}]`), &items); err != nil {
		t.Fatal(err)
	}
	if got, want := len(items), 3; got != want {
		t.Fatalf("items len = %d, want %d", got, want)
	}
	if items[2].ID != 3 {
		t.Fatalf("items = %+v", items)
	}

	var enveloped []struct {
		Name string `json:"name"`
	}
	if err := DecodeCLIJSONEnvelopePageStream([]byte(`{"records":[{"name":"a"}]}
{"records":[{"name":"b"}]}`), "records", &enveloped); err != nil {
		t.Fatal(err)
	}
	if got, want := len(enveloped), 2; got != want {
		t.Fatalf("enveloped len = %d, want %d", got, want)
	}
	if enveloped[1].Name != "b" {
		t.Fatalf("enveloped = %+v", enveloped)
	}
}

type fakeAPIAdapter struct {
	subcommand []string
}

func (a fakeAPIAdapter) BuildCommand(identity ExternalCLIIdentity, hostArgs []string, request ExternalCLIAPIRequest) (ExternalCLICommand, error) {
	body, err := request.EncodedBody()
	if err != nil {
		return ExternalCLICommand{}, err
	}
	args := append([]string{}, a.subcommand...)
	args = append(args, hostArgs...)
	args = append(args, "--method", request.Method)
	query := request.Query.Encode()
	if query != "" {
		for _, part := range strings.Split(query, "&") {
			args = append(args, "--query", part)
		}
	}
	if request.Paginate {
		args = append(args, "--paginate")
	}
	args = append(args, request.Endpoint)
	return ExternalCLICommand{Binary: identity.Binary, Args: args, Stdin: body}, nil
}
