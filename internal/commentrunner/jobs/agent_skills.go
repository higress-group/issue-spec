package jobs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const agentSkillEntrypoint = "SKILL.md"

// materializeTrustedAgentSkills copies operator-selected skill roots into the
// isolated CODEX_HOME used by one runner session. A repository checkout is not
// an input here: only explicit runner configuration can introduce a skill.
func materializeTrustedAgentSkills(codexHome string, sourceDirs []string) error {
	if len(sourceDirs) == 0 {
		return nil
	}
	if strings.TrimSpace(codexHome) == "" {
		return errors.New("runtime CODEX_HOME is required for trusted agent skills")
	}
	skills, err := collectTrustedAgentSkills(sourceDirs)
	if err != nil {
		return err
	}
	codexHome = filepath.Clean(codexHome)
	if err := ensureRuntimeSkillDirectory(codexHome); err != nil {
		return fmt.Errorf("validate runtime CODEX_HOME: %w", err)
	}
	targetRoot := filepath.Join(codexHome, "skills")
	if err := ensureRuntimeSkillDirectory(targetRoot); err != nil {
		return fmt.Errorf("validate runtime skill root: %w", err)
	}
	for _, skill := range skills {
		target := filepath.Join(targetRoot, skill.name)
		if err := replaceTrustedSkill(skill.source, target); err != nil {
			return fmt.Errorf("materialize trusted agent skill %s: %w", skill.name, err)
		}
	}
	return nil
}

type trustedAgentSkill struct {
	name   string
	source string
}

func collectTrustedAgentSkills(sourceDirs []string) ([]trustedAgentSkill, error) {
	byName := map[string]string{}
	for _, source := range sourceDirs {
		source = filepath.Clean(strings.TrimSpace(source))
		info, err := os.Lstat(source)
		if err != nil {
			return nil, fmt.Errorf("inspect trusted skill directory %s: %w", source, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("trusted skill directory %s must be a non-symlink directory", source)
		}
		if hasTrustedSkillEntrypoint(source) {
			if err := addTrustedAgentSkill(byName, filepath.Base(source), source); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			candidate := filepath.Join(source, entry.Name())
			info, err := os.Lstat(candidate)
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("trusted skill catalog contains symlink %s", candidate)
			}
			if !info.IsDir() || !hasTrustedSkillEntrypoint(candidate) {
				continue
			}
			if err := addTrustedAgentSkill(byName, entry.Name(), candidate); err != nil {
				return nil, err
			}
		}
	}
	if len(byName) == 0 {
		return nil, errors.New("no non-symlink skill directory containing SKILL.md was found")
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]trustedAgentSkill, 0, len(names))
	for _, name := range names {
		result = append(result, trustedAgentSkill{name: name, source: byName[name]})
	}
	return result, nil
}

func hasTrustedSkillEntrypoint(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, agentSkillEntrypoint))
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func addTrustedAgentSkill(byName map[string]string, name, source string) error {
	if name == "." || name == "" || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid trusted skill name %q", name)
	}
	if previous, exists := byName[name]; exists && previous != source {
		return fmt.Errorf("trusted skill name %q is supplied by both %s and %s", name, previous, source)
	}
	byName[name] = source
	return nil
}

func replaceTrustedSkill(source, target string) error {
	targetRoot := filepath.Dir(target)
	if err := ensureRuntimeSkillDirectory(targetRoot); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("runtime skill destination %s is not a directory", target)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staging, err := os.MkdirTemp(targetRoot, ".skill-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := copyTrustedSkill(source, staging); err != nil {
		return err
	}
	if err := ensureRuntimeSkillDirectory(targetRoot); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("runtime skill destination %s is not a directory", target)
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		return err
	}
	return nil
}

func copyTrustedSkill(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("resolve trusted skill path %s", path)
		}
		destination := target
		if rel != "." {
			destination = filepath.Join(target, rel)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("trusted skill contains symlink %s", path)
		}
		if info.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("trusted skill contains non-regular file %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		return os.WriteFile(destination, data, mode)
	})
}

func ensureRuntimeSkillDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a non-symlink directory", path)
	}
	return nil
}
