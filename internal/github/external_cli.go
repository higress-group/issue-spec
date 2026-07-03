package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
)

// External CLI primitives are provider-neutral. GitHub backends adapt these
// pieces to the GitHub-specific Operations interface in separate files.
type ExternalCLIIdentity struct {
	Name   string
	Binary string
}

type ExternalCLIProbe struct {
	Name  string
	Args  []string
	Stdin []byte
}

type ExternalCLIDescriptor struct {
	Identity     ExternalCLIIdentity
	VersionProbe ExternalCLIProbe
	AuthProbe    ExternalCLIProbe
	HostAdapter  ExternalCLIHostAdapter
	APIAdapter   ExternalCLIAPIAdapter
	ErrorAdapter ExternalCLIErrorAdapter
}

func (d ExternalCLIDescriptor) Validate() error {
	if strings.TrimSpace(d.Identity.Name) == "" {
		return fmt.Errorf("external CLI name is required")
	}
	if strings.TrimSpace(d.Identity.Binary) == "" {
		return fmt.Errorf("external CLI binary is required")
	}
	if d.APIAdapter == nil {
		return fmt.Errorf("external CLI API adapter is required")
	}
	return nil
}

type ExternalCLI struct {
	Descriptor ExternalCLIDescriptor
	Runner     ExternalCLIRunner
	Redactor   ExternalCLIRedactor
}

func NewExternalCLI(descriptor ExternalCLIDescriptor, runner ExternalCLIRunner, redactor ExternalCLIRedactor) (*ExternalCLI, error) {
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = ExecCLIRunner{}
	}
	if descriptor.ErrorAdapter == nil {
		descriptor.ErrorAdapter = DefaultExternalCLIErrorAdapter{}
	}
	return &ExternalCLI{Descriptor: descriptor, Runner: runner, Redactor: redactor}, nil
}

func (c *ExternalCLI) RunProbe(ctx context.Context, host string, probe ExternalCLIProbe) (ExternalCLIResult, error) {
	hostArgs, err := c.hostArgs(host)
	if err != nil {
		return ExternalCLIResult{}, err
	}
	args := append(append([]string{}, hostArgs...), probe.Args...)
	command := ExternalCLICommand{
		Binary:    c.Descriptor.Identity.Binary,
		Args:      args,
		Stdin:     append([]byte(nil), probe.Stdin...),
		Operation: probe.Name,
		Host:      normalizeHost(host),
	}
	return c.run(ctx, command)
}

func (c *ExternalCLI) RunAPI(ctx context.Context, host string, request ExternalCLIAPIRequest) (ExternalCLIResult, error) {
	if err := request.Validate(); err != nil {
		return ExternalCLIResult{}, err
	}
	hostArgs, err := c.hostArgs(host)
	if err != nil {
		return ExternalCLIResult{}, err
	}
	command, err := c.Descriptor.APIAdapter.BuildCommand(c.Descriptor.Identity, hostArgs, request)
	if err != nil {
		return ExternalCLIResult{}, err
	}
	command.Operation = request.Operation
	command.Host = normalizeHost(host)
	command.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	command.Endpoint = strings.TrimSpace(request.Endpoint)
	return c.run(ctx, command)
}

func (c *ExternalCLI) hostArgs(host string) ([]string, error) {
	if c.Descriptor.HostAdapter == nil {
		return nil, nil
	}
	return c.Descriptor.HostAdapter.HostArgs(host)
}

func (c *ExternalCLI) run(ctx context.Context, command ExternalCLICommand) (ExternalCLIResult, error) {
	result, err := c.Runner.RunCLI(ctx, command)
	if err != nil || result.ExitCode != 0 {
		return result, c.Descriptor.ErrorAdapter.CommandError(c.Descriptor, command, result, err, c.Redactor)
	}
	return result, nil
}

type ExternalCLICommand struct {
	Binary    string
	Args      []string
	Env       []string
	Stdin     []byte
	Operation string
	Host      string
	Method    string
	Endpoint  string
}

func (c ExternalCLICommand) Render(redactor ExternalCLIRedactor) string {
	parts := make([]string, 0, 1+len(c.Args))
	parts = append(parts, c.Binary)
	parts = append(parts, c.Args...)
	for i, part := range parts {
		parts[i] = quoteCommandPart(redactor.Redact(part))
	}
	return strings.Join(parts, " ")
}

type ExternalCLIResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type ExternalCLIRunner interface {
	RunCLI(context.Context, ExternalCLICommand) (ExternalCLIResult, error)
}

type ExecCLIRunner struct{}

func (ExecCLIRunner) RunCLI(ctx context.Context, command ExternalCLICommand) (ExternalCLIResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, command.Binary, command.Args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if command.Stdin != nil {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}
	if len(command.Env) > 0 {
		cmd.Env = append(os.Environ(), command.Env...)
	}
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return ExternalCLIResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}, err
}

type ExternalCLIHostAdapter interface {
	HostArgs(host string) ([]string, error)
}

type HostFlagAdapter struct {
	Flag        string
	DefaultHost string
	OmitDefault bool
}

func (a HostFlagAdapter) HostArgs(host string) ([]string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = strings.TrimSpace(a.DefaultHost)
	}
	if host == "" {
		return nil, nil
	}
	if a.OmitDefault && strings.EqualFold(host, strings.TrimSpace(a.DefaultHost)) {
		return nil, nil
	}
	flag := strings.TrimSpace(a.Flag)
	if flag == "" {
		return nil, fmt.Errorf("external CLI host flag is required for host %q", host)
	}
	return []string{flag, host}, nil
}

type ExternalCLIAPIRequest struct {
	Operation string
	Method    string
	Endpoint  string
	Headers   http.Header
	Query     url.Values
	Body      any
	Paginate  bool
	Include   bool
}

func (r ExternalCLIAPIRequest) Validate() error {
	if strings.TrimSpace(r.Method) == "" {
		return fmt.Errorf("external CLI API method is required")
	}
	if strings.TrimSpace(r.Endpoint) == "" {
		return fmt.Errorf("external CLI API endpoint is required")
	}
	return nil
}

func (r ExternalCLIAPIRequest) EncodedBody() ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return json.Marshal(r.Body)
}

type ExternalCLIAPIAdapter interface {
	BuildCommand(ExternalCLIIdentity, []string, ExternalCLIAPIRequest) (ExternalCLICommand, error)
}

type ExternalCLIErrorAdapter interface {
	CommandError(ExternalCLIDescriptor, ExternalCLICommand, ExternalCLIResult, error, ExternalCLIRedactor) error
}

type DefaultExternalCLIErrorAdapter struct{}

func (DefaultExternalCLIErrorAdapter) CommandError(descriptor ExternalCLIDescriptor, command ExternalCLICommand, result ExternalCLIResult, runErr error, redactor ExternalCLIRedactor) error {
	return &ExternalCLIError{
		Backend: descriptor.Identity.Name,
		Command: command,
		Result:  result,
		Err:     runErr,
		redact:  redactor,
	}
}

type ExternalCLIError struct {
	Backend string
	Command ExternalCLICommand
	Result  ExternalCLIResult
	Err     error
	redact  ExternalCLIRedactor
}

func (e *ExternalCLIError) Error() string {
	backend := strings.TrimSpace(e.Backend)
	if backend == "" {
		backend = "external CLI"
	}
	parts := []string{fmt.Sprintf("%s command failed", backend)}
	if e.Result.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit %d", e.Result.ExitCode))
	}
	if operation := strings.TrimSpace(e.Command.Operation); operation != "" {
		parts = append(parts, "operation "+operation)
	}
	if host := strings.TrimSpace(e.Command.Host); host != "" {
		parts = append(parts, "host "+host)
	}
	if method := strings.TrimSpace(e.Command.Method); method != "" {
		parts = append(parts, "method "+method)
	}
	if endpoint := strings.TrimSpace(e.Command.Endpoint); endpoint != "" {
		parts = append(parts, "endpoint "+endpoint)
	}
	parts = append(parts, e.Command.Render(e.redact))
	stderr := strings.TrimSpace(e.redact.Redact(string(e.Result.Stderr)))
	if stderr != "" {
		parts = append(parts, stderr)
	}
	if e.Err != nil {
		parts = append(parts, e.redact.Redact(e.Err.Error()))
	}
	return strings.Join(parts, ": ")
}

func (e *ExternalCLIError) Unwrap() error {
	return e.Err
}

type ExternalCLIRedactor struct {
	values []string
}

func NewExternalCLIRedactor(values ...string) ExternalCLIRedactor {
	out := ExternalCLIRedactor{}
	return out.WithValues(values...)
}

func (r ExternalCLIRedactor) WithValues(values ...string) ExternalCLIRedactor {
	out := ExternalCLIRedactor{values: append([]string{}, r.values...)}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out.values = append(out.values, value)
		}
	}
	return out
}

func (r ExternalCLIRedactor) Redact(value string) string {
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func DecodeCLIJSON(data []byte, out any) error {
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return io.EOF
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode external CLI JSON: %w", err)
	}
	return nil
}

func DecodeCLIJSONPageStream(data []byte, out any) error {
	return decodeCLIJSONPages(data, "", out)
}

func DecodeCLIJSONEnvelopePageStream(data []byte, envelopeKey string, out any) error {
	if strings.TrimSpace(envelopeKey) == "" {
		return fmt.Errorf("external CLI JSON envelope key is required")
	}
	return decodeCLIJSONPages(data, envelopeKey, out)
}

func decodeCLIJSONPages(data []byte, envelopeKey string, out any) error {
	target, err := sliceTarget(out)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode external CLI JSON page: %w", err)
		}
		if envelopeKey != "" {
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return fmt.Errorf("decode external CLI JSON envelope: %w", err)
			}
			page, ok := envelope[envelopeKey]
			if !ok {
				return fmt.Errorf("external CLI JSON envelope missing key %q", envelopeKey)
			}
			raw = page
		}
		if err := appendJSONArray(target, raw); err != nil {
			return err
		}
	}
}

func sliceTarget(out any) (reflect.Value, error) {
	if out == nil {
		return reflect.Value{}, fmt.Errorf("external CLI JSON page output is nil")
	}
	target := reflect.ValueOf(out)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return reflect.Value{}, fmt.Errorf("external CLI JSON page output must be a non-nil pointer to slice")
	}
	target = target.Elem()
	if target.Kind() != reflect.Slice {
		return reflect.Value{}, fmt.Errorf("external CLI JSON page output must point to a slice")
	}
	return target, nil
}

func appendJSONArray(target reflect.Value, raw json.RawMessage) error {
	pagePtr := reflect.New(target.Type())
	if err := json.Unmarshal(raw, pagePtr.Interface()); err != nil {
		return fmt.Errorf("decode external CLI JSON array page: %w", err)
	}
	target.Set(reflect.AppendSlice(target, pagePtr.Elem()))
	return nil
}

func quoteCommandPart(part string) string {
	if part == "" {
		return `""`
	}
	if strings.ContainsAny(part, " \t\n\"'\\") {
		return strconv.Quote(part)
	}
	return part
}
