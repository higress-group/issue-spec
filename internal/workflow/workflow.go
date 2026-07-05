package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const BuiltinSchemaName = "issue-spec"

type SourceKind string

const (
	SourceIssueSpecProject SourceKind = "issue-spec-project"
	SourceLegacyOpenSpec   SourceKind = "legacy-openspec"
	SourceUser             SourceKind = "user"
	SourceBuiltin          SourceKind = "builtin"
)

type Diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	Source   string `json:"source,omitempty"`
}

type Config struct {
	Schema     string         `json:"schema,omitempty" yaml:"schema"`
	Context    map[string]any `json:"context,omitempty" yaml:"context"`
	Rules      map[string]any `json:"rules,omitempty" yaml:"rules"`
	References []string       `json:"references,omitempty" yaml:"references"`
	Store      map[string]any `json:"store,omitempty" yaml:"store"`
}

type Source struct {
	Kind        SourceKind `json:"kind"`
	ConfigPath  string     `json:"config_path,omitempty"`
	SchemaPath  string     `json:"schema_path,omitempty"`
	TemplateDir string     `json:"template_dir,omitempty"`
	SchemaName  string     `json:"schema_name"`
}

type Plan struct {
	Source      Source       `json:"source"`
	Config      Config       `json:"config"`
	Schema      Schema       `json:"schema"`
	Artifacts   []Artifact   `json:"artifacts"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type Schema struct {
	Name string `json:"name"`
}

type Artifact struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Template      string   `json:"template,omitempty"`
	TemplatePath  string   `json:"template_path,omitempty"`
	Generates     []string `json:"generates,omitempty"`
	Dependencies  []string `json:"dependencies,omitempty"`
	Instructions  string   `json:"instructions,omitempty"`
	ApplyTracks   string   `json:"apply_tracks,omitempty"`
	Storage       []string `json:"storage,omitempty"`
	UnknownFields []string `json:"unknown_fields,omitempty"`
}

type ResolveOptions struct {
	Root          string
	Schema        string
	UserConfigDir string
}

func Resolve(root string) (Plan, error) {
	return ResolveWithOptions(ResolveOptions{Root: root})
}

func ResolveWithOptions(opts ResolveOptions) (Plan, error) {
	root := opts.Root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	root = filepath.Clean(root)
	plan := Plan{}

	issueConfig := filepath.Join(root, "issue-spec", "config.yaml")
	legacyConfig := filepath.Join(root, "openspec", "config.yaml")
	hasIssueConfig := fileExists(issueConfig)
	hasLegacyConfig := fileExists(legacyConfig)

	switch {
	case hasIssueConfig:
		plan.Source.Kind = SourceIssueSpecProject
		plan.Source.ConfigPath = filepath.ToSlash(issueConfig)
		if hasLegacyConfig {
			plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
				Severity: "warning",
				Code:     "legacy_config_ignored",
				Message:  "openspec/config.yaml is ignored because issue-spec/config.yaml is present",
				Path:     filepath.ToSlash(legacyConfig),
			})
		}
		cfg, err := readConfig(issueConfig)
		if err != nil {
			return withError(plan, Diagnostic{Severity: "error", Code: "invalid_config", Message: err.Error(), Path: filepath.ToSlash(issueConfig)})
		}
		plan.Config = cfg
	case hasLegacyConfig:
		plan.Source.Kind = SourceLegacyOpenSpec
		plan.Source.ConfigPath = filepath.ToSlash(legacyConfig)
		cfg, err := readConfig(legacyConfig)
		if err != nil {
			return withError(plan, Diagnostic{Severity: "error", Code: "invalid_config", Message: err.Error(), Path: filepath.ToSlash(legacyConfig)})
		}
		plan.Config = cfg
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Severity: "warning",
			Code:     "legacy_openspec_mode",
			Message:  "legacy OpenSpec workflow definition selected; active artifacts remain in GitHub issue-native storage",
			Path:     filepath.ToSlash(legacyConfig),
		})
	default:
		plan.Source.Kind = SourceBuiltin
	}

	if schemaOverride := strings.TrimSpace(opts.Schema); schemaOverride != "" {
		plan.Config.Schema = schemaOverride
	}
	schemaName := strings.TrimSpace(plan.Config.Schema)
	if schemaName == "" {
		schemaName = BuiltinSchemaName
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
			Severity: "info",
			Code:     "default_schema_selected",
			Message:  "no schema selected; using built-in issue-spec workflow schema",
		})
	}
	plan.Source.SchemaName = schemaName

	artifacts, schemaPath, templateDir, sourceKind, err := resolveSchema(root, opts.UserConfigDir, plan.Source.Kind, schemaName)
	if err != nil {
		available := strings.Join(availableSchemas(root, opts.UserConfigDir), ", ")
		msg := err.Error()
		if available != "" {
			msg += "; available schemas: " + available
		}
		return withError(plan, Diagnostic{Severity: "error", Code: "schema_not_found", Message: msg, Source: string(plan.Source.Kind)})
	}
	if sourceKind != "" {
		plan.Source.Kind = sourceKind
	}
	plan.Source.SchemaPath = filepath.ToSlash(schemaPath)
	plan.Source.TemplateDir = filepath.ToSlash(templateDir)
	plan.Schema = Schema{Name: schemaName}
	plan.Artifacts = artifacts
	plan.Diagnostics = append(plan.Diagnostics, shadowedSchemaDiagnostics(root, opts.UserConfigDir, schemaName, schemaPath)...)

	if diags := validateArtifacts(templateDir, plan.Artifacts, plan.Source.Kind); len(diags) > 0 {
		plan.Diagnostics = append(plan.Diagnostics, diags...)
	}
	if plan.HasErrors() {
		return plan, errors.New("workflow validation failed")
	}
	return plan, nil
}

func (p Plan) HasErrors() bool {
	for _, d := range p.Diagnostics {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func (p Plan) ArtifactForIssue(kind string) (Artifact, bool) {
	want := strings.ToLower(strings.TrimSpace(kind))
	for _, artifact := range p.Artifacts {
		if strings.EqualFold(artifact.Type, want) || strings.EqualFold(artifact.ID, want) {
			if artifact.TemplatePath != "" {
				return artifact, true
			}
		}
	}
	return Artifact{}, false
}

func (p Plan) ArtifactForComment(commentType string) (Artifact, bool) {
	want := strings.ToUpper(strings.TrimSpace(commentType))
	for _, artifact := range p.Artifacts {
		if strings.ToUpper(normalizeArtifactType(artifact.Type, artifact.ID, artifact.Generates)) == want {
			if artifact.TemplatePath != "" {
				return artifact, true
			}
		}
	}
	return Artifact{}, false
}

func RenderTemplate(path string, data any) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tmpl, err := template.New(filepath.Base(path)).Option("missingkey=zero").Parse(string(raw))
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

type ArchivePathSelection struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Legacy bool   `json:"legacy,omitempty"`
}

func SelectArchivePath(root, capability, explicit string) ArchivePathSelection {
	if value := strings.TrimSpace(explicit); value != "" {
		return ArchivePathSelection{Path: filepath.Clean(value), Source: "explicit"}
	}
	legacy := filepath.Join(root, "openspec", "specs", capability, "spec.md")
	if fileExists(legacy) {
		path := filepath.Join("openspec", "specs", capability, "spec.md")
		return ArchivePathSelection{Path: filepath.Clean(path), Source: "legacy-existing", Legacy: true}
	}
	return ArchivePathSelection{
		Path:   filepath.Join("issue-spec", "specs", capability, "spec.md"),
		Source: "default",
	}
}

func withError(plan Plan, diagnostic Diagnostic) (Plan, error) {
	plan.Diagnostics = append(plan.Diagnostics, diagnostic)
	return plan, errors.New(diagnostic.Message)
}

func readConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func resolveSchema(root, userConfigDir string, source SourceKind, schemaName string) ([]Artifact, string, string, SourceKind, error) {
	if source == SourceIssueSpecProject {
		if artifacts, schemaPath, templateDir, ok, err := loadSchema(filepath.Join(root, "issue-spec", "schemas", schemaName)); ok || err != nil {
			return artifacts, schemaPath, templateDir, SourceIssueSpecProject, err
		}
	}
	if source == SourceLegacyOpenSpec {
		if artifacts, schemaPath, templateDir, ok, err := loadSchema(filepath.Join(root, "openspec", "schemas", schemaName)); ok || err != nil {
			return artifacts, schemaPath, templateDir, SourceLegacyOpenSpec, err
		}
	}
	if userConfigDir == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			userConfigDir = dir
		}
	}
	if userConfigDir != "" {
		if artifacts, schemaPath, templateDir, ok, err := loadSchema(filepath.Join(userConfigDir, "issue-spec", "schemas", schemaName)); ok || err != nil {
			return artifacts, schemaPath, templateDir, SourceUser, err
		}
	}
	if schemaName == BuiltinSchemaName {
		return builtinArtifacts(), "", "", SourceBuiltin, nil
	}
	return nil, "", "", "", fmt.Errorf("schema %q was not found", schemaName)
}

func loadSchema(schemaDir string) ([]Artifact, string, string, bool, error) {
	schemaPath := filepath.Join(schemaDir, "schema.yaml")
	if !fileExists(schemaPath) {
		return nil, "", "", false, nil
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, schemaPath, "", true, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, schemaPath, "", true, err
	}
	artifacts, err := parseArtifacts(schemaPath, &node)
	if err != nil {
		return nil, schemaPath, "", true, err
	}
	templateDir := filepath.Join(schemaDir, "templates")
	for i := range artifacts {
		artifacts[i].Type = normalizeArtifactType(artifacts[i].Type, artifacts[i].ID, artifacts[i].Generates)
		artifacts[i].Storage = storageMappings(artifacts[i])
		if artifacts[i].Template != "" {
			artifacts[i].TemplatePath = filepath.Join(templateDir, filepath.Clean(artifacts[i].Template))
		}
	}
	return artifacts, schemaPath, templateDir, true, nil
}

func parseArtifacts(schemaPath string, root *yaml.Node) ([]Artifact, error) {
	doc := root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: schema root must be a mapping", schemaPath)
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "artifacts" {
			return decodeArtifacts(doc.Content[i+1])
		}
	}
	return nil, nil
}

type rawArtifact struct {
	ID           string    `yaml:"id"`
	Name         string    `yaml:"name"`
	Type         string    `yaml:"type"`
	Template     string    `yaml:"template"`
	Instructions string    `yaml:"instructions"`
	Apply        rawApply  `yaml:"apply"`
	Generates    yaml.Node `yaml:"generates"`
	Dependencies yaml.Node `yaml:"dependencies"`
	DependsOn    yaml.Node `yaml:"depends_on"`
	DependsOnAlt yaml.Node `yaml:"dependsOn"`
}

type rawApply struct {
	Tracks string `yaml:"tracks"`
}

func decodeArtifacts(node *yaml.Node) ([]Artifact, error) {
	switch node.Kind {
	case yaml.MappingNode:
		out := make([]Artifact, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			id := strings.TrimSpace(node.Content[i].Value)
			artifact, err := decodeArtifact(node.Content[i+1], id)
			if err != nil {
				return nil, err
			}
			out = append(out, artifact)
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]Artifact, 0, len(node.Content))
		for _, item := range node.Content {
			artifact, err := decodeArtifact(item, "")
			if err != nil {
				return nil, err
			}
			out = append(out, artifact)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("artifacts must be a mapping or sequence")
	}
}

func decodeArtifact(node *yaml.Node, id string) (Artifact, error) {
	var raw rawArtifact
	if err := node.Decode(&raw); err != nil {
		return Artifact{}, err
	}
	if raw.ID != "" {
		id = raw.ID
	}
	if id == "" {
		id = raw.Name
	}
	artifact := Artifact{
		ID:            id,
		Type:          raw.Type,
		Template:      strings.TrimSpace(raw.Template),
		Generates:     stringList(raw.Generates),
		Instructions:  strings.TrimSpace(raw.Instructions),
		ApplyTracks:   strings.TrimSpace(raw.Apply.Tracks),
		UnknownFields: unknownArtifactFields(node),
	}
	artifact.Dependencies = stringList(firstNonEmptyNode(raw.Dependencies, raw.DependsOn, raw.DependsOnAlt))
	return artifact, nil
}

func unknownArtifactFields(node *yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	known := map[string]bool{
		"id":           true,
		"name":         true,
		"type":         true,
		"template":     true,
		"instructions": true,
		"apply":        true,
		"generates":    true,
		"dependencies": true,
		"depends_on":   true,
		"dependsOn":    true,
	}
	var fields []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := strings.TrimSpace(node.Content[i].Value)
		if key != "" && !known[key] {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

func stringList(node yaml.Node) []string {
	switch node.Kind {
	case yaml.ScalarNode:
		if value := strings.TrimSpace(node.Value); value != "" {
			return []string{value}
		}
	case yaml.SequenceNode:
		var out []string
		for _, item := range node.Content {
			if value := strings.TrimSpace(item.Value); value != "" {
				out = append(out, value)
			}
		}
		return out
	}
	return nil
}

func firstNonEmptyNode(nodes ...yaml.Node) yaml.Node {
	for _, node := range nodes {
		if node.Kind != 0 {
			return node
		}
	}
	return yaml.Node{}
}

func validateArtifacts(templateDir string, artifacts []Artifact, source SourceKind) []Diagnostic {
	var diagnostics []Diagnostic
	ids := map[string]bool{}
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.ID) == "" {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "artifact_missing_id", Message: "artifact is missing id"})
		}
		if ids[artifact.ID] {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "duplicate_artifact_id", Artifact: artifact.ID, Message: "duplicate artifact id " + artifact.ID})
		}
		ids[artifact.ID] = true
		if !supportedArtifactType(artifact.Type) {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "unsupported_artifact_type", Artifact: artifact.ID, Message: fmt.Sprintf("artifact %s has unsupported type %q", artifact.ID, artifact.Type)})
		}
		for _, field := range artifact.UnknownFields {
			severity := "warning"
			code := "unknown_artifact_field"
			if requiredLikeArtifactField(field) {
				severity = "error"
				code = "unsupported_artifact_field"
			}
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Code: code, Artifact: artifact.ID, Message: fmt.Sprintf("artifact %s has unsupported field %q", artifact.ID, field)})
		}
		for _, dep := range artifact.Dependencies {
			if !ids[dep] {
				// Complete missing-dependency validation after id scan below.
				continue
			}
		}
		if artifact.Template != "" {
			diagnostics = append(diagnostics, validateTemplatePath(templateDir, artifact)...)
			diagnostics = append(diagnostics, scanTemplateForLegacyWarnings(artifact)...)
		}
		diagnostics = append(diagnostics, scanTextForLegacyWarnings(artifact, artifact.Instructions)...)
		if source == SourceLegacyOpenSpec {
			for _, output := range artifact.Generates {
				if strings.Contains(output, "openspec/changes") {
					diagnostics = append(diagnostics, Diagnostic{Severity: "warning", Code: "legacy_active_storage_hint", Artifact: artifact.ID, Message: "legacy OpenSpec active change output is treated as an issue-native storage hint"})
				}
			}
		}
	}
	for _, artifact := range artifacts {
		for _, dep := range artifact.Dependencies {
			if !ids[dep] {
				diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "missing_dependency", Artifact: artifact.ID, Message: fmt.Sprintf("artifact %s depends on missing artifact %s", artifact.ID, dep)})
			}
		}
	}
	diagnostics = append(diagnostics, cycleDiagnostics(artifacts)...)
	return diagnostics
}

func requiredLikeArtifactField(field string) bool {
	field = strings.ToLower(strings.TrimSpace(field))
	return strings.Contains(field, "require") ||
		strings.Contains(field, "storage") ||
		strings.Contains(field, "output") ||
		strings.Contains(field, "write")
}

func validateTemplatePath(templateDir string, artifact Artifact) []Diagnostic {
	templatePath := artifact.Template
	if filepath.IsAbs(templatePath) {
		return []Diagnostic{{Severity: "error", Code: "unsafe_template_path", Artifact: artifact.ID, Path: templatePath, Message: "template path must be relative"}}
	}
	clean := filepath.Clean(templatePath)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return []Diagnostic{{Severity: "error", Code: "unsafe_template_path", Artifact: artifact.ID, Path: templatePath, Message: "template path must not escape the schema template directory"}}
	}
	path := filepath.Join(templateDir, clean)
	if !fileExists(path) {
		return []Diagnostic{{Severity: "error", Code: "missing_template", Artifact: artifact.ID, Path: filepath.ToSlash(path), Message: "template file is missing"}}
	}
	baseEval, baseErr := filepath.EvalSymlinks(templateDir)
	pathEval, pathErr := filepath.EvalSymlinks(path)
	if baseErr == nil && pathErr == nil {
		rel, err := filepath.Rel(baseEval, pathEval)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return []Diagnostic{{Severity: "error", Code: "unsafe_template_path", Artifact: artifact.ID, Path: filepath.ToSlash(path), Message: "template symlink escapes schema template directory"}}
		}
	}
	return nil
}

func scanTemplateForLegacyWarnings(artifact Artifact) []Diagnostic {
	if artifact.TemplatePath == "" {
		return nil
	}
	data, err := os.ReadFile(artifact.TemplatePath)
	if err != nil {
		return nil
	}
	return scanTextForLegacyWarnings(artifact, string(data))
}

func scanTextForLegacyWarnings(artifact Artifact, text string) []Diagnostic {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "openspec/changes") || strings.Contains(lower, "openspec changes") {
		return []Diagnostic{{Severity: "warning", Code: "openspec_only_instruction", Artifact: artifact.ID, Path: filepath.ToSlash(artifact.TemplatePath), Message: "template or instruction mentions OpenSpec active-change storage; issue-spec maps active artifacts to GitHub issue-native storage"}}
	}
	return nil
}

func cycleDiagnostics(artifacts []Artifact) []Diagnostic {
	graph := map[string][]string{}
	for _, artifact := range artifacts {
		graph[artifact.ID] = artifact.Dependencies
	}
	var diagnostics []Diagnostic
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "dependency_cycle", Artifact: id, Message: "artifact dependency graph contains a cycle at " + id})
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dep := range graph[id] {
			if graph[dep] != nil {
				visit(dep)
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for id := range graph {
		visit(id)
	}
	return diagnostics
}

func normalizeArtifactType(typ, id string, generates []string) string {
	value := strings.ToLower(strings.TrimSpace(typ))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(id))
	}
	if value == "" {
		for _, output := range generates {
			lower := strings.ToLower(output)
			switch {
			case strings.Contains(lower, "proposal"):
				value = "proposal"
			case strings.Contains(lower, "design"):
				value = "design"
			case strings.Contains(lower, "task"):
				value = "task"
			case strings.Contains(lower, "review"):
				value = "review"
			case strings.Contains(lower, "verify"):
				value = "verify"
			case strings.Contains(lower, "spec"):
				value = "SPEC"
			}
		}
	}
	switch value {
	case "spec", "specs", "requirement", "requirements":
		return "SPEC"
	case "question", "questions":
		return "QUESTION"
	case "task", "tasks":
		return "TASK"
	case "process", "apply":
		return "PROCESS"
	case "review":
		return "REVIEW"
	case "verify", "verification":
		return "VERIFY"
	case "proposal", "design", "implement", "rationale", "finding", "archive":
		return value
	default:
		if strings.HasPrefix(strings.ToUpper(value), "SPEC") {
			return "SPEC"
		}
		return value
	}
}

func supportedArtifactType(typ string) bool {
	switch typ {
	case "proposal", "design", "implement", "SPEC", "QUESTION", "TASK", "PROCESS", "REVIEW", "VERIFY", "rationale", "finding", "archive":
		return true
	default:
		return false
	}
}

func storageMappings(artifact Artifact) []string {
	seen := map[string]bool{}
	add := func(value string) {
		if value != "" {
			seen[value] = true
		}
	}
	switch artifact.Type {
	case "proposal":
		add("proposal-issue-body")
	case "design":
		add("design-issue-body")
	case "implement":
		add("implement-issue-body")
	case "SPEC":
		add("SPEC-typed-comment")
	case "QUESTION":
		add("QUESTION-typed-comment")
	case "TASK":
		add("TASK-typed-comment")
	case "PROCESS":
		add("PROCESS-typed-comment")
	case "REVIEW":
		add("REVIEW-typed-comment")
		add("pr-review-comment")
	case "VERIFY":
		add("VERIFY-typed-comment")
	case "rationale":
		add("pr-rationale-comment")
	case "finding":
		add("pr-review-comment")
	case "archive":
		add("durable-archive-output")
	}
	for _, output := range artifact.Generates {
		lower := strings.ToLower(output)
		switch {
		case strings.Contains(lower, "proposal"):
			add("proposal-issue-body")
		case strings.Contains(lower, "design"):
			add("design-issue-body")
		case strings.Contains(lower, "implement"):
			add("implement-issue-body")
		case strings.Contains(lower, "tasks"):
			add("TASK-typed-comment")
		case strings.Contains(lower, "review"):
			add("REVIEW-typed-comment")
			add("pr-review-comment")
		case strings.Contains(lower, "verify"):
			add("VERIFY-typed-comment")
		case strings.Contains(lower, "spec"):
			add("SPEC-typed-comment")
			add("durable-archive-output")
		}
	}
	if artifact.ApplyTracks != "" {
		add("TASK-typed-comment")
		add("PROCESS-typed-comment")
		add("issue-spec-links")
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func builtinArtifacts() []Artifact {
	artifacts := []Artifact{
		{ID: "proposal", Type: "proposal", Storage: []string{"proposal-issue-body"}},
		{ID: "specs", Type: "SPEC", Storage: []string{"SPEC-typed-comment"}},
		{ID: "questions", Type: "QUESTION", Storage: []string{"QUESTION-typed-comment"}},
		{ID: "design", Type: "design", Storage: []string{"design-issue-body"}},
		{ID: "tasks", Type: "TASK", Storage: []string{"TASK-typed-comment"}},
		{ID: "process", Type: "PROCESS", Storage: []string{"PROCESS-typed-comment"}},
		{ID: "review", Type: "REVIEW", Storage: []string{"REVIEW-typed-comment", "pr-review-comment"}},
		{ID: "verify", Type: "VERIFY", Storage: []string{"VERIFY-typed-comment"}},
		{ID: "archive", Type: "archive", Storage: []string{"durable-archive-output"}},
	}
	return artifacts
}

func availableSchemas(root, userConfigDir string) []string {
	seen := map[string]bool{}
	addDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() && fileExists(filepath.Join(dir, entry.Name(), "schema.yaml")) {
				seen[entry.Name()] = true
			}
		}
	}
	addDir(filepath.Join(root, "issue-spec", "schemas"))
	addDir(filepath.Join(root, "openspec", "schemas"))
	if userConfigDir == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			userConfigDir = dir
		}
	}
	if userConfigDir != "" {
		addDir(filepath.Join(userConfigDir, "issue-spec", "schemas"))
	}
	seen[BuiltinSchemaName] = true
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func shadowedSchemaDiagnostics(root, userConfigDir, schemaName, selectedPath string) []Diagnostic {
	candidates := []struct {
		source SourceKind
		path   string
	}{
		{SourceIssueSpecProject, filepath.Join(root, "issue-spec", "schemas", schemaName, "schema.yaml")},
		{SourceLegacyOpenSpec, filepath.Join(root, "openspec", "schemas", schemaName, "schema.yaml")},
	}
	if userConfigDir == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			userConfigDir = dir
		}
	}
	if userConfigDir != "" {
		candidates = append(candidates, struct {
			source SourceKind
			path   string
		}{SourceUser, filepath.Join(userConfigDir, "issue-spec", "schemas", schemaName, "schema.yaml")})
	}
	var diagnostics []Diagnostic
	selectedClean := filepath.Clean(selectedPath)
	for _, candidate := range candidates {
		if selectedClean != "" && filepath.Clean(candidate.path) == selectedClean {
			continue
		}
		if fileExists(candidate.path) {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "warning",
				Code:     "schema_shadowed",
				Message:  fmt.Sprintf("schema %q at %s is shadowed by %s", schemaName, candidate.source, selectedClean),
				Path:     filepath.ToSlash(candidate.path),
				Source:   string(candidate.source),
			})
		}
	}
	return diagnostics
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
