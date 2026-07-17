package requirements

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Target string

const (
	TargetCodex  Target = "codex"
	TargetClaude Target = "claude"
)

type InstallAction string

const (
	ActionCreate         InstallAction = "create"
	ActionNoop           InstallAction = "no-op"
	ActionManagedUpgrade InstallAction = "managed-upgrade"
	ActionUserModified   InstallAction = "user-modified"
)

type ConflictDecision string

const (
	ConflictReplace   ConflictDecision = "replace"
	ConflictAlternate ConflictDecision = "alternate-destination"
	ConflictCancel    ConflictDecision = "cancel"
)

var (
	ErrPlanChanged  = errors.New("requirements skill install plan changed")
	ErrUserModified = errors.New("requirements skill target is user-modified")
	ErrCancelled    = errors.New("requirements skill installation cancelled")
)

// TargetOptions resolves an explicit install destination without reading process
// environment implicitly. Callers pass CODEX_HOME, CLAUDE_CONFIG_DIR, and HOME
// after presenting their source to the user. TargetDir is the expert override.
type TargetOptions struct {
	Home            string
	CodexHome       string
	ClaudeConfigDir string
	TargetDir       string
}

// InstallPlan is a read-only preview. Path is always absolute, so callers can
// display the exact global write before asking for confirmation.
type InstallPlan struct {
	Target           Target        `json:"target"`
	Path             string        `json:"path"`
	Action           InstallAction `json:"action"`
	ContentID        string        `json:"content_id"`
	CurrentContentID string        `json:"current_content_id,omitempty"`
	Reason           string        `json:"reason,omitempty"`

	observedFingerprint string
	replaceConfirmed    bool
}

type InstallResult struct {
	Target         Target        `json:"target"`
	Path           string        `json:"path"`
	Action         InstallAction `json:"action"`
	ContentID      string        `json:"content_id"`
	Changed        bool          `json:"changed"`
	CleanupWarning string        `json:"cleanup_warning,omitempty"`
}

// ResolveTarget returns the exact Codex or Claude skill directory.
func ResolveTarget(target Target, options TargetOptions) (string, error) {
	if target != TargetCodex && target != TargetClaude {
		return "", fmt.Errorf("unsupported requirements skill target %q", target)
	}
	if strings.TrimSpace(options.TargetDir) != "" {
		return absoluteInstallPath(options.TargetDir)
	}
	home := strings.TrimSpace(options.Home)
	if home == "" {
		return "", errors.New("home directory is required when no explicit target directory is supplied")
	}
	var root string
	switch target {
	case TargetCodex:
		root = strings.TrimSpace(options.CodexHome)
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
	case TargetClaude:
		root = strings.TrimSpace(options.ClaudeConfigDir)
		if root == "" {
			root = filepath.Join(home, ".claude")
		}
	}
	return absoluteInstallPath(filepath.Join(root, "skills", SkillName))
}

// PreviewInstall classifies the target without writing it.
func PreviewInstall(bundle Bundle, target Target, options TargetOptions, cliVersion string) (InstallPlan, error) {
	if err := validateBundle(bundle); err != nil {
		return InstallPlan{}, fmt.Errorf("invalid requirements skill bundle: %w", err)
	}
	if err := bundle.CheckCompatibility(cliVersion); err != nil {
		return InstallPlan{}, err
	}
	targetPath, err := ResolveTarget(target, options)
	if err != nil {
		return InstallPlan{}, err
	}
	return previewPath(bundle, target, targetPath)
}

// ApplyConflictDecision records an explicit replace, returns a separately
// previewable alternate plan, or cancels without writing. An alternate plan is
// intentionally not auto-installed: callers must display and confirm it.
func ApplyConflictDecision(bundle Bundle, plan InstallPlan, cliVersion string, decision ConflictDecision, alternatePath string) (InstallPlan, error) {
	if err := validateBundle(bundle); err != nil {
		return InstallPlan{}, fmt.Errorf("invalid requirements skill bundle: %w", err)
	}
	if plan.Action != ActionUserModified {
		return InstallPlan{}, errors.New("conflict decision is only valid for a user-modified target")
	}
	switch decision {
	case ConflictReplace:
		plan.replaceConfirmed = true
		return plan, nil
	case ConflictAlternate:
		if strings.TrimSpace(alternatePath) == "" {
			return InstallPlan{}, errors.New("alternate destination is required")
		}
		if err := bundle.CheckCompatibility(cliVersion); err != nil {
			return InstallPlan{}, err
		}
		resolved, err := absoluteInstallPath(alternatePath)
		if err != nil {
			return InstallPlan{}, err
		}
		alternate, err := previewPath(bundle, plan.Target, resolved)
		if err != nil {
			return InstallPlan{}, err
		}
		alternate.Reason = strings.TrimSpace(alternate.Reason + "; alternate destination requires its own confirmation")
		return alternate, nil
	case ConflictCancel:
		return InstallPlan{}, ErrCancelled
	default:
		return InstallPlan{}, fmt.Errorf("unsupported conflict decision %q", decision)
	}
}

// Install applies a previously displayed plan. It fails closed if any target
// byte changed after preview.
func Install(bundle Bundle, plan InstallPlan) (InstallResult, error) {
	if err := validateBundle(bundle); err != nil {
		return InstallResult{}, fmt.Errorf("invalid requirements skill bundle: %w", err)
	}
	if plan.ContentID != bundle.Manifest.ContentID || plan.Path == "" || plan.observedFingerprint == "" {
		return InstallResult{}, errors.New("install requires a plan produced for this exact bundle")
	}
	if plan.Action == ActionUserModified && !plan.replaceConfirmed {
		return InstallResult{}, ErrUserModified
	}
	observed, err := inspectTarget(plan.Path, bundle)
	if err != nil {
		return InstallResult{}, err
	}
	if observed.fingerprint != plan.observedFingerprint || observed.action != plan.Action {
		return InstallResult{}, ErrPlanChanged
	}
	result := InstallResult{Target: plan.Target, Path: plan.Path, Action: plan.Action, ContentID: plan.ContentID}
	if plan.Action == ActionNoop {
		return result, nil
	}

	parent := filepath.Dir(plan.Path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("create requirements skill parent directory: %w", err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(plan.Path)+".stage-")
	if err != nil {
		return InstallResult{}, fmt.Errorf("create requirements skill staging directory: %w", err)
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := writeBundle(staging, bundle); err != nil {
		return InstallResult{}, err
	}
	staged, err := inspectTarget(staging, bundle)
	if err != nil || staged.action != ActionNoop {
		if err == nil {
			err = fmt.Errorf("staged requirements skill does not match the canonical bundle: action=%s reason=%s", staged.action, staged.reason)
		}
		return InstallResult{}, err
	}

	if plan.Action == ActionCreate {
		current, err := inspectTarget(plan.Path, bundle)
		if err != nil {
			return InstallResult{}, err
		}
		if current.fingerprint != plan.observedFingerprint || current.action != ActionCreate {
			return InstallResult{}, ErrPlanChanged
		}
		if err := os.Rename(staging, plan.Path); err != nil {
			return InstallResult{}, fmt.Errorf("activate requirements skill: %w", err)
		}
		removeStaging = false
		installed, err := inspectTarget(plan.Path, bundle)
		if err != nil || installed.action != ActionNoop {
			_ = os.RemoveAll(plan.Path)
			if err == nil {
				err = errors.New("activated requirements skill failed validation")
			}
			return InstallResult{}, err
		}
		result.Changed = true
		return result, nil
	}

	backup, err := reserveSibling(parent, "."+filepath.Base(plan.Path)+".backup-")
	if err != nil {
		return InstallResult{}, err
	}
	if err := os.Rename(plan.Path, backup); err != nil {
		return InstallResult{}, fmt.Errorf("preserve previous requirements skill: %w", err)
	}
	backupFingerprint, err := fingerprintTree(backup)
	if err != nil || backupFingerprint != plan.observedFingerprint {
		restoreErr := os.Rename(backup, plan.Path)
		if err == nil {
			err = ErrPlanChanged
		}
		if restoreErr != nil {
			return InstallResult{}, fmt.Errorf("%w; restore previous target: %v", err, restoreErr)
		}
		return InstallResult{}, err
	}
	if err := os.Rename(staging, plan.Path); err != nil {
		restoreErr := os.Rename(backup, plan.Path)
		if restoreErr != nil {
			return InstallResult{}, fmt.Errorf("activate requirements skill: %w; restore previous target: %v", err, restoreErr)
		}
		return InstallResult{}, fmt.Errorf("activate requirements skill: %w", err)
	}
	removeStaging = false
	installed, validationErr := inspectTarget(plan.Path, bundle)
	if validationErr != nil || installed.action != ActionNoop {
		failed := staging + ".failed"
		moveErr := os.Rename(plan.Path, failed)
		restoreErr := os.Rename(backup, plan.Path)
		_ = os.RemoveAll(failed)
		if validationErr == nil {
			validationErr = errors.New("activated requirements skill failed validation")
		}
		if moveErr != nil || restoreErr != nil {
			return InstallResult{}, fmt.Errorf("%w; move failed install: %v; restore previous target: %v", validationErr, moveErr, restoreErr)
		}
		return InstallResult{}, validationErr
	}
	result.Changed = true
	finalBackupFingerprint, fingerprintErr := fingerprintTree(backup)
	if fingerprintErr != nil || finalBackupFingerprint != plan.observedFingerprint {
		result.CleanupWarning = fmt.Sprintf("installed successfully but retained changed backup %s", backup)
		return result, nil
	}
	if err := os.RemoveAll(backup); err != nil {
		result.CleanupWarning = fmt.Sprintf("installed successfully but could not remove backup %s: %v", backup, err)
	}
	return result, nil
}

type targetInspection struct {
	action           InstallAction
	currentContentID string
	reason           string
	fingerprint      string
}

func previewPath(bundle Bundle, target Target, targetPath string) (InstallPlan, error) {
	inspection, err := inspectTarget(targetPath, bundle)
	if err != nil {
		return InstallPlan{}, err
	}
	return InstallPlan{
		Target: target, Path: targetPath, Action: inspection.action, ContentID: bundle.Manifest.ContentID,
		CurrentContentID: inspection.currentContentID, Reason: inspection.reason, observedFingerprint: inspection.fingerprint,
	}, nil
}

func inspectTarget(targetPath string, expected Bundle) (targetInspection, error) {
	fingerprint, err := fingerprintTree(targetPath)
	if errors.Is(err, fs.ErrNotExist) {
		return targetInspection{action: ActionCreate, fingerprint: "missing", reason: "target does not exist"}, nil
	}
	if err != nil {
		return targetInspection{}, fmt.Errorf("inspect requirements skill target: %w", err)
	}
	files, directories, err := readInstalledFiles(targetPath)
	if err != nil {
		return targetInspection{action: ActionUserModified, fingerprint: fingerprint, reason: err.Error()}, nil
	}
	if filesEqual(files, expected.Files) && directoriesEqual(directories, expectedDirectories(expected.Files)) {
		return targetInspection{action: ActionNoop, currentContentID: expected.Manifest.ContentID, fingerprint: fingerprint, reason: "target exactly matches this release"}, nil
	}
	manifestFile, ok := fileNamed(files, ManagedManifestName)
	if !ok {
		return targetInspection{action: ActionUserModified, fingerprint: fingerprint, reason: "managed manifest is missing"}, nil
	}
	manifest, err := decodeManifest(manifestFile.Data)
	if err != nil {
		return targetInspection{action: ActionUserModified, fingerprint: fingerprint, reason: "managed manifest is invalid"}, nil
	}
	current := Bundle{Manifest: manifest, Files: files}
	if err := validateBundle(current); err != nil || !directoriesEqual(directories, expectedDirectories(files)) {
		reason := "installed files differ from their managed manifest"
		if err == nil {
			reason = "installed directory contains unmanaged directories"
		}
		return targetInspection{action: ActionUserModified, currentContentID: manifest.ContentID, fingerprint: fingerprint, reason: reason}, nil
	}
	return targetInspection{action: ActionManagedUpgrade, currentContentID: manifest.ContentID, fingerprint: fingerprint, reason: "target is an unmodified issue-spec-managed version"}, nil
}

func writeBundle(root string, bundle Bundle) error {
	for _, file := range bundle.Files {
		if err := validateRelativePath(file.Path); err != nil {
			return err
		}
		destination := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("create staged skill directory: %w", err)
		}
		handle, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.Mode.Perm())
		if err != nil {
			return fmt.Errorf("create staged skill file %q: %w", file.Path, err)
		}
		_, writeErr := handle.Write(file.Data)
		if writeErr == nil {
			writeErr = handle.Sync()
		}
		closeErr := handle.Close()
		if writeErr != nil {
			return fmt.Errorf("write staged skill file %q: %w", file.Path, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close staged skill file %q: %w", file.Path, closeErr)
		}
		// OpenFile honors the process umask. Normalize after the write so the
		// staged tree matches the canonical manifest on every host.
		if err := os.Chmod(destination, file.Mode.Perm()); err != nil {
			return fmt.Errorf("set staged skill file %q permissions: %w", file.Path, err)
		}
	}
	if err := os.Chmod(root, 0o755); err != nil {
		return fmt.Errorf("set staged skill directory permissions: %w", err)
	}
	return nil
}

func readInstalledFiles(root string) ([]File, []string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("target is not a regular directory")
	}
	var files []File
	var directories []string
	var total int64
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target contains symbolic link %q", relative)
		}
		if entry.IsDir() {
			directories = append(directories, relative)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("target contains non-regular file %q", relative)
		}
		if info.Size() > archiveSizeLimit || total+info.Size() > archiveSizeLimit {
			return errors.New("target skill tree exceeds the size limit")
		}
		total += info.Size()
		handle, err := os.Open(current)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(handle, archiveSizeLimit+1))
		closeErr := handle.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		files = append(files, File{Path: relative, Data: data, Mode: info.Mode().Perm()})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sortFiles(files)
	sort.Strings(directories)
	return files, directories, nil
}

func fingerprintTree(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, ".\x00%o\x00%d\n", info.Mode(), info.Size())
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%o\x00%d\x00", relative, info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			hash.Write([]byte(target))
		} else if info.Mode().IsRegular() {
			handle, err := os.Open(current)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, handle)
			closeErr := handle.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		hash.Write([]byte{'\n'})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func filesEqual(left, right []File) bool {
	if len(left) != len(right) {
		return false
	}
	left = cloneFiles(left)
	right = cloneFiles(right)
	sortFiles(left)
	sortFiles(right)
	for index := range left {
		if left[index].Path != right[index].Path || left[index].Mode.Perm() != right[index].Mode.Perm() || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}

func expectedDirectories(files []File) []string {
	set := map[string]struct{}{}
	for _, file := range files {
		current := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file.Path)))
		for current != "." && current != "/" {
			set[current] = struct{}{}
			current = filepath.ToSlash(filepath.Dir(filepath.FromSlash(current)))
		}
	}
	result := make([]string, 0, len(set))
	for directory := range set {
		result = append(result, directory)
	}
	sort.Strings(result)
	return result
}

func directoriesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func absoluteInstallPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("requirements skill target directory is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve requirements skill target: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == filepath.Dir(absolute) || filepath.Base(absolute) == "." {
		return "", errors.New("requirements skill target must name a bounded directory")
	}
	return absolute, nil
}

func reserveSibling(parent, pattern string) (string, error) {
	reserved, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", fmt.Errorf("reserve requirements skill backup path: %w", err)
	}
	if err := os.Remove(reserved); err != nil {
		return "", fmt.Errorf("prepare requirements skill backup path: %w", err)
	}
	return reserved, nil
}
