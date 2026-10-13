package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/buildinfo"
	"github.com/higress-group/issue-spec/internal/workflow"
)

// Workflow updates are planned entirely before touching the checkout. The
// release manifest owns files, never a skill directory or operator additions.
type workflowFileChange struct {
	Path         string `json:"path"`
	Action       string `json:"action"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	body         []byte
	link         string
}
type workflowUpdatePlan struct {
	Changes    []workflowFileChange     `json:"changes"`
	Conflicts  []string                 `json:"conflicts"`
	Generation workflowGenerationResult `json:"generation"`
}

const workflowManifestPath = ".agents/skills/issue-spec-workflow/release.json"

func workflowHash(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }

func planWorkflowUpdate(root, repo, delivery string, tools []workflowTool, plan workflow.Plan, provider *workflow.ProviderPlan) (workflowUpdatePlan, error) {
	update := workflowUpdatePlan{Changes: []workflowFileChange{}, Conflicts: []string{}}
	stage, err := os.MkdirTemp("", "issue-spec-workflow-render-")
	if err != nil {
		return update, err
	}
	defer os.RemoveAll(stage)
	// Keep installed references portable across repository clones/worktrees.
	absoluteRoot, _ := filepath.Abs(root)
	for _, value := range []*string{&plan.Source.ConfigPath, &plan.Source.TemplateDir, &plan.Source.SchemaPath} {
		if filepath.IsAbs(*value) {
			if relative, e := filepath.Rel(absoluteRoot, *value); e == nil && filepath.IsLocal(relative) {
				*value = filepath.ToSlash(relative)
			}
		}
	}
	generated, err := renderWorkflowArtifacts(stage, repo, delivery, tools, plan, provider)
	if err != nil {
		return update, err
	}
	update.Generation = generated
	// Report checkout paths, never temporary staging locations.
	for _, paths := range []*[]string{&update.Generation.SkillFiles, &update.Generation.SkillResourceFiles, &update.Generation.CommandFiles} {
		for i, path := range *paths {
			rel, e := filepath.Rel(stage, path)
			if e != nil {
				return update, e
			}
			(*paths)[i] = cleanGeneratedPath(filepath.Join(root, rel))
		}
	}
	oldFiles := map[string]string{}
	manifestFile := filepath.Join(root, workflowManifestPath)
	if raw, e := os.ReadFile(manifestFile); e == nil {
		var manifest workflowReleaseManifest
		if e = json.Unmarshal(raw, &manifest); e != nil || manifest.Schema != workflowReleaseManifestSchema || manifest.ContentDigest != workflowReleaseContentDigest(manifest.Files) {
			return update, fmt.Errorf("invalid workflow release manifest; reconcile it before updating")
		}
		for _, file := range manifest.Files {
			if !workflowManagedPath(file.Path) || oldFiles[file.Path] != "" {
				return update, fmt.Errorf("invalid managed path %q", file.Path)
			}
			oldFiles[file.Path] = file.SHA256
		}
	} else if !os.IsNotExist(e) {
		return update, e
	}
	desired := map[string][]byte{}
	err = filepath.WalkDir(stage, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, e := filepath.Rel(stage, path)
		if e != nil {
			return e
		}
		body, e := os.ReadFile(path)
		desired[filepath.ToSlash(rel)] = body
		return e
	})
	if err != nil {
		return update, err
	}
	if delivery == workflowDeliveryCommands && len(desired) > 0 {
		// Legacy command-only delivery has its own managed writes but must retain
		// integrity records for previously installed canonical skills.
		hashes := map[string]string{}
		for path, hash := range oldFiles {
			if strings.HasPrefix(path, ".agents/") {
				hashes[path] = hash
			}
		}
		for path, body := range desired {
			hashes[path] = workflowHash(body)
		}
		keys := make([]string, 0, len(hashes))
		for path := range hashes {
			keys = append(keys, path)
		}
		sort.Strings(keys)
		files := make([]workflowReleaseFile, 0, len(keys))
		for _, path := range keys {
			files = append(files, workflowReleaseFile{Path: path, SHA256: hashes[path]})
		}
		identity := buildinfo.Current()
		manifest := workflowReleaseManifest{Schema: workflowReleaseManifestSchema, Release: identity.Version, SourceRevision: identity.Revision, RequirementsSkillContentID: identity.RequirementsSkillContentID, Files: files, ContentDigest: workflowReleaseContentDigest(files)}
		raw, e := json.MarshalIndent(manifest, "", "  ")
		if e != nil {
			return update, e
		}
		desired[workflowManifestPath] = append(raw, '\n')
	}
	paths := map[string]bool{}
	for path := range oldFiles {
		paths[path] = true
	}
	for path := range desired {
		paths[path] = true
	}
	for _, path := range sortedWorkflowPaths(paths) {
		if delivery == workflowDeliveryCommands && strings.HasPrefix(path, ".agents/") && path != workflowManifestPath {
			continue
		}
		if err := workflowRegularParents(root, path); err != nil {
			update.Conflicts = append(update.Conflicts, err.Error())
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		info, e := os.Lstat(target)
		if e != nil && !os.IsNotExist(e) {
			return update, e
		}
		exists := e == nil
		if exists && !info.Mode().IsRegular() {
			update.Conflicts = append(update.Conflicts, path+": expected a regular managed file")
			continue
		}
		var before []byte
		if exists {
			before, e = os.ReadFile(target)
			if e != nil {
				return update, e
			}
		}
		after, wanted := desired[path]
		if exists && wanted && string(before) == string(after) {
			continue
		}
		if !exists && !wanted {
			continue
		}
		if path != workflowManifestPath {
			expected, managed := oldFiles[path]
			if (exists && (!managed || workflowHash(before) != expected)) || (!exists && managed && wanted) {
				update.Conflicts = append(update.Conflicts, path+": locally changed or not managed; move project guidance out and reconcile before updating")
				continue
			}
		}
		change := workflowFileChange{Path: path, Action: "create", body: after}
		if exists {
			change.Action = "update"
			change.BeforeSHA256 = workflowHash(before)
		}
		if !wanted {
			change.Action = "remove"
		} else {
			change.AfterSHA256 = workflowHash(after)
		}
		update.Changes = append(update.Changes, change)
	}
	if delivery != workflowDeliveryCommands && workflowToolSelected(tools, "claude") {
		if err := planClaudeSkillLinks(root, stage, &update); err != nil {
			return update, err
		}
	}
	if delivery != workflowDeliveryCommands && workflowToolSelected(tools, "claude") {
		for _, path := range sortedWorkflowPaths(paths) {
			if strings.HasPrefix(path, ".agents/skills/") && strings.HasSuffix(path, "/SKILL.md") {
				if _, kept := desired[path]; kept {
					continue
				}
				name := strings.Split(path, "/")[2]
				linkPath := filepath.Join(".claude/skills", name)
				info, e := os.Lstat(filepath.Join(root, linkPath))
				if e != nil || info.Mode()&os.ModeSymlink == 0 {
					continue
				}
				matches, _, e := symlinkMatchesPath(filepath.Join(root, linkPath), filepath.Join(root, ".agents/skills", name))
				if e == nil && matches {
					update.Changes = append(update.Changes, workflowFileChange{Path: linkPath, Action: "remove"})
				}
			}
		}
	}
	return update, nil
}

func sortedWorkflowPaths(paths map[string]bool) []string {
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func workflowRegularParents(root, path string) error {
	for dir := filepath.Dir(path); dir != "."; dir = filepath.Dir(dir) {
		info, err := os.Lstat(filepath.Join(root, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: parent must be a real directory", dir)
		}
	}
	return nil
}

func planClaudeSkillLinks(root, stage string, update *workflowUpdatePlan) error {
	const directory = ".claude/skills"
	if err := workflowRegularParents(root, directory); err != nil {
		update.Conflicts = append(update.Conflicts, err.Error())
		return nil
	}
	info, err := os.Lstat(filepath.Join(root, directory))
	wholeLink := err == nil && info.Mode()&os.ModeSymlink != 0
	names := map[string]bool{}
	if wholeLink {
		matches, _, e := symlinkMatchesPath(filepath.Join(root, directory), filepath.Join(root, ".agents/skills"))
		if e != nil || !matches {
			update.Conflicts = append(update.Conflicts, directory+": unexpected symlink target")
			return nil
		}
		entries, e := os.ReadDir(filepath.Join(root, ".agents/skills"))
		if e != nil {
			return e
		}
		for _, entry := range entries {
			names[entry.Name()] = true
		}
		// Preserve precisely the pre-existing exposure when migrating the old
		// whole-directory link. Subsequent updates add only generated skills.
		update.Changes = append(update.Changes, workflowFileChange{Path: directory, Action: "split-link"})
	} else if err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil && !info.IsDir() {
		update.Conflicts = append(update.Conflicts, directory+": expected directory")
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(stage, ".agents/skills"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	for _, name := range sortedWorkflowPaths(names) {
		path := filepath.Join(directory, name)
		target := "../../.agents/skills/" + name
		if !wholeLink {
			info, e := os.Lstat(filepath.Join(root, path))
			if e != nil && !os.IsNotExist(e) {
				return e
			}
			if e == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					matches, _, e := symlinkMatchesPath(filepath.Join(root, path), filepath.Join(root, ".agents/skills", name))
					if e == nil && matches {
						continue
					}
				} else if info.IsDir() {
					// Only replace an exact redundant copy, including its supporting files.
					forward, e := directoryTreeCoveredByTarget(filepath.Join(root, path), filepath.Join(root, ".agents/skills", name))
					reverse, e2 := directoryTreeCoveredByTarget(filepath.Join(root, ".agents/skills", name), filepath.Join(root, path))
					if e == nil && e2 == nil && forward && reverse {
						update.Changes = append(update.Changes, workflowFileChange{Path: path, Action: "link", link: target})
						continue
					}
				}
				update.Conflicts = append(update.Conflicts, path+": differs from the canonical skill; preserve or reconcile before linking")
				continue
			}
		}
		update.Changes = append(update.Changes, workflowFileChange{Path: path, Action: "link", link: target})
	}
	return nil
}

// Move each replaced entry into a sibling backup before installing it. A failed
// write rolls back all completed moves, including directory/link migrations.
func applyWorkflowUpdate(root string, update workflowUpdatePlan) (err error) {
	if len(update.Conflicts) > 0 {
		return fmt.Errorf("workflow update conflicts:\n%s", strings.Join(update.Conflicts, "\n"))
	}
	if len(update.Changes) == 0 {
		return nil
	}
	backup, err := os.MkdirTemp(root, ".issue-spec-update-")
	if err != nil {
		return err
	}
	rollbackFailed := false
	defer func() {
		if !rollbackFailed {
			_ = os.RemoveAll(backup)
		}
	}()
	type applied struct{ path, saved string }
	var done []applied
	var createdDirs []string
	defer func() {
		if err == nil {
			return
		}
		for i := len(done) - 1; i >= 0; i-- {
			entry := done[i]
			if e := os.RemoveAll(entry.path); e != nil {
				rollbackFailed = true
				err = fmt.Errorf("%w; rollback remove (backup %s): %v", err, backup, e)
			}
			if entry.saved != "" {
				if e := os.Rename(entry.saved, entry.path); e != nil {
					rollbackFailed = true
					err = fmt.Errorf("%w; rollback restore (backup %s): %v", err, backup, e)
				}
			}
		}
		for _, dir := range createdDirs {
			_ = os.Remove(dir)
		}
	}()
	for _, change := range update.Changes {
		if change.Action == "create" {
			if _, e := os.Lstat(filepath.Join(root, change.Path)); !os.IsNotExist(e) {
				return fmt.Errorf("%s appeared after planning", change.Path)
			}
		}
		if change.BeforeSHA256 != "" {
			body, e := os.ReadFile(filepath.Join(root, change.Path))
			if e != nil || workflowHash(body) != change.BeforeSHA256 {
				return fmt.Errorf("%s changed after planning", change.Path)
			}
		}
	}
	for i, change := range update.Changes {
		path := filepath.Join(root, filepath.FromSlash(change.Path))
		entry := applied{path: path}
		if _, e := os.Lstat(path); e == nil {
			entry.saved = filepath.Join(backup, fmt.Sprint(i))
			if err = os.Rename(path, entry.saved); err != nil {
				return err
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		done = append(done, entry)
		if change.Action == "remove" {
			continue
		}
		for dir := filepath.Dir(path); dir != filepath.Clean(root) && dir != "."; dir = filepath.Dir(dir) {
			if _, e := os.Lstat(dir); os.IsNotExist(e) {
				createdDirs = append(createdDirs, dir)
			} else {
				break
			}
		}
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		switch change.Action {
		case "split-link":
			err = os.Mkdir(path, 0755)
		case "link":
			err = os.Symlink(change.link, path)
		default:
			err = os.WriteFile(path, change.body, 0644)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeWorkflowArtifactsResolvedWithProvider(root, repo, delivery string, tools []workflowTool, plan workflow.Plan, provider *workflow.ProviderPlan) (workflowGenerationResult, error) {
	if len(tools) == 0 {
		return workflowGenerationResult{Delivery: delivery}, nil
	}
	update, err := planWorkflowUpdate(root, repo, delivery, tools, plan, provider)
	if err != nil {
		return update.Generation, err
	}
	if err = applyWorkflowUpdate(root, update); err != nil {
		return update.Generation, err
	}
	for _, change := range update.Changes {
		if change.Action == "remove" {
			update.Generation.PrunedFiles = append(update.Generation.PrunedFiles, cleanGeneratedPath(filepath.Join(root, change.Path)))
		}
	}
	return update.Generation, nil
}

func workflowManagedPath(path string) bool {
	if !filepath.IsLocal(path) || strings.Contains(path, "\\") || filepath.ToSlash(filepath.Clean(path)) != path {
		return false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	if len(parts) >= 4 && parts[0] == ".agents" && parts[1] == "skills" && strings.HasPrefix(parts[2], "issue-spec-") {
		return true
	}
	return len(parts) == 4 && parts[0] == ".claude" && parts[1] == "commands" && parts[2] == "issue-spec" && strings.HasSuffix(parts[3], ".md")
}
