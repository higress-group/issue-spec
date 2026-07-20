package filecas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileImage is one exact filesystem representation. Exists is kept separate
// from Digest so a missing file cannot be confused with an empty file.
type FileImage struct {
	Exists  bool   `json:"exists"`
	Digest  string `json:"digest,omitempty"`
	Content string `json:"content,omitempty"`
}

// FileMutation is a repository-relative whole-file compare-and-swap. The
// preimage contains identity only; the postimage carries the bytes to install.
type FileMutation struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Preimage  FileImage `json:"preimage"`
	Postimage FileImage `json:"postimage"`
}

type FileOperationResult struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Status       string `json:"status"`
	BeforeDigest string `json:"before_digest,omitempty"`
	AfterDigest  string `json:"after_digest,omitempty"`
}

type FileApplyResult struct {
	OK         bool                  `json:"ok"`
	Updated    int                   `json:"updated"`
	Unchanged  int                   `json:"unchanged"`
	Conflicted int                   `json:"conflicted"`
	Operations []FileOperationResult `json:"operations"`
}

// FileDigest is the stable representation digest used by durable plans.
func FileDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func ImageForContent(content []byte) FileImage {
	return FileImage{Exists: true, Digest: FileDigest(content), Content: string(content)}
}

func MissingFileImage() FileImage { return FileImage{} }

// ValidateFileMutations returns mutations in deterministic path order.
func ValidateFileMutations(mutations []FileMutation) ([]FileMutation, error) {
	ordered := append([]FileMutation(nil), mutations...)
	seenID, seenPath := map[string]bool{}, map[string]bool{}
	for index := range ordered {
		mutation := &ordered[index]
		mutation.ID = strings.TrimSpace(mutation.ID)
		mutation.Path = filepath.ToSlash(strings.TrimSpace(mutation.Path))
		if mutation.ID == "" || mutation.Path == "" {
			return nil, errors.New("file mutation id and path are required")
		}
		if seenID[mutation.ID] {
			return nil, fmt.Errorf("duplicate file mutation id %q", mutation.ID)
		}
		if seenPath[mutation.Path] {
			return nil, fmt.Errorf("duplicate file mutation path %q", mutation.Path)
		}
		seenID[mutation.ID], seenPath[mutation.Path] = true, true
		if err := validateRelativeFilePath(mutation.Path); err != nil {
			return nil, fmt.Errorf("file mutation %s: %w", mutation.ID, err)
		}
		if err := validateFileImage("preimage", mutation.Preimage, false); err != nil {
			return nil, fmt.Errorf("file mutation %s: %w", mutation.ID, err)
		}
		if err := validateFileImage("postimage", mutation.Postimage, true); err != nil {
			return nil, fmt.Errorf("file mutation %s: %w", mutation.ID, err)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered, nil
}

func validateFileImage(name string, image FileImage, requireContent bool) error {
	if !image.Exists {
		if image.Digest != "" || image.Content != "" {
			return fmt.Errorf("%s for a missing file must not carry digest or content", name)
		}
		if requireContent {
			return fmt.Errorf("%s must exist", name)
		}
		return nil
	}
	if !isSHA256(image.Digest) {
		return fmt.Errorf("%s digest must be 64 lowercase hexadecimal characters", name)
	}
	if requireContent && FileDigest([]byte(image.Content)) != image.Digest {
		return fmt.Errorf("%s content does not match its digest", name)
	}
	if !requireContent && image.Content != "" {
		return fmt.Errorf("%s must not embed content", name)
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func validateRelativeFilePath(value string) error {
	if strings.Contains(value, `\`) || filepath.IsAbs(value) || filepath.Clean(value) != filepath.FromSlash(value) ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("path %q must be a clean repository-relative path", value)
	}
	return nil
}

// ApplyFileMutations re-observes every file before the first write, accepts
// only exact preimages or exact planned postimages, and then installs pending
// postimages through same-directory temporary-file replacement. A completed
// postimage is an idempotent retry, including when only some earlier files were
// installed by a prior attempt.
func ApplyFileMutations(root string, mutations []FileMutation) (FileApplyResult, error) {
	ordered, err := ValidateFileMutations(mutations)
	if err != nil {
		return FileApplyResult{}, err
	}
	root, err = secureFileRoot(root)
	if err != nil {
		return FileApplyResult{}, err
	}
	result := FileApplyResult{OK: true}
	states := make([]FileImage, len(ordered))
	for index, mutation := range ordered {
		observed, observeErr := observeFileImage(root, mutation.Path)
		if observeErr != nil {
			return FileApplyResult{}, fmt.Errorf("observe %s: %w", mutation.Path, observeErr)
		}
		states[index] = observed
		if sameFileImage(observed, mutation.Preimage) || sameFileImage(observed, mutation.Postimage) {
			continue
		}
		result.OK = false
		result.Conflicted++
		result.Operations = append(result.Operations, FileOperationResult{ID: mutation.ID, Path: mutation.Path,
			Status: "conflicted", BeforeDigest: observed.Digest, AfterDigest: mutation.Postimage.Digest})
	}
	if result.Conflicted != 0 {
		return result, errors.New("file CAS preflight found unrecognized target state; no files were written")
	}

	for index, mutation := range ordered {
		if sameFileImage(states[index], mutation.Postimage) {
			result.Unchanged++
			result.Operations = append(result.Operations, FileOperationResult{ID: mutation.ID, Path: mutation.Path,
				Status: "unchanged", BeforeDigest: mutation.Postimage.Digest, AfterDigest: mutation.Postimage.Digest})
			continue
		}
		// Re-observe immediately before replacement so an edit after the global
		// preflight fails closed instead of being knowingly overwritten.
		current, observeErr := observeFileImage(root, mutation.Path)
		if observeErr != nil {
			return result, fmt.Errorf("reobserve %s: %w", mutation.Path, observeErr)
		}
		if sameFileImage(current, mutation.Postimage) {
			result.Unchanged++
			result.Operations = append(result.Operations, FileOperationResult{ID: mutation.ID, Path: mutation.Path,
				Status: "unchanged", BeforeDigest: current.Digest, AfterDigest: current.Digest})
			continue
		}
		if !sameFileImage(current, mutation.Preimage) {
			result.OK = false
			result.Conflicted++
			result.Operations = append(result.Operations, FileOperationResult{ID: mutation.ID, Path: mutation.Path,
				Status: "conflicted", BeforeDigest: current.Digest, AfterDigest: mutation.Postimage.Digest})
			return result, fmt.Errorf("file CAS target %s changed after preflight", mutation.Path)
		}
		if err := replaceFileAtomically(root, mutation.Path, []byte(mutation.Postimage.Content)); err != nil {
			result.OK = false
			return result, fmt.Errorf("replace %s: %w", mutation.Path, err)
		}
		result.Updated++
		result.Operations = append(result.Operations, FileOperationResult{ID: mutation.ID, Path: mutation.Path,
			Status: "updated", BeforeDigest: mutation.Preimage.Digest, AfterDigest: mutation.Postimage.Digest})
	}
	return result, nil
}

func secureFileRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository root must be an existing directory")
	}
	return resolved, nil
}

func observeFileImage(root, relative string) (FileImage, error) {
	target, err := secureTargetPath(root, relative, false)
	if err != nil {
		return FileImage{}, err
	}
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return MissingFileImage(), nil
	}
	if err != nil {
		return FileImage{}, err
	}
	return FileImage{Exists: true, Digest: FileDigest(data)}, nil
}

func secureTargetPath(root, relative string, createParents bool) (string, error) {
	if err := validateRelativeFilePath(relative); err != nil {
		return "", err
	}
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if createParents && index < len(parts)-1 {
				if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
					return "", err
				}
				continue
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path %q contains symlink component %q", relative, part)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("path %q has non-directory component %q", relative, part)
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("path %q is not a regular file", relative)
		}
	}
	return filepath.Join(root, filepath.FromSlash(relative)), nil
}

func replaceFileAtomically(root, relative string, data []byte) error {
	target, err := secureTargetPath(root, relative, true)
	if err != nil {
		return err
	}
	directory := filepath.Dir(target)
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(target); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	temporary, err := os.CreateTemp(directory, ".issue-spec-durable-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return err
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		defer directoryHandle.Close()
		_ = directoryHandle.Sync()
	}
	return nil
}

func sameFileImage(left, right FileImage) bool {
	return left.Exists == right.Exists && left.Digest == right.Digest
}
