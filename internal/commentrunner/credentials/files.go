package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const IssueTokenSandboxPath = "/run/issue-spec/credentials/issue.token"

type FileLease struct {
	HostPath    string
	SandboxPath string
}

type Materializer struct{ Root string }

// WriteProfileToken materializes the long-lived, origin-bound PAT selected by
// runner serve at one stable path. Unlike job credentials, this file is not
// removed when an individual job finishes, so resumed ACPX sessions keep the
// same credential source across turns.
func (m Materializer) WriteProfileToken(token string) (FileLease, error) {
	if invalidToken(token) {
		return FileLease{}, errors.New("credential materializer: profile token is required")
	}
	root, err := m.secureRoot()
	if err != nil {
		return FileLease{}, err
	}
	path := filepath.Join(root, "profile.token")
	if err := atomicSecretFile(path, []byte(strings.TrimSpace(token)+"\n")); err != nil {
		return FileLease{}, err
	}
	return FileLease{HostPath: path, SandboxPath: IssueTokenSandboxPath}, nil
}

func (m Materializer) WriteIssueToken(jobID, token string) (FileLease, error) {
	if strings.TrimSpace(jobID) == "" || invalidToken(token) {
		return FileLease{}, errors.New("credential materializer: job id and token are required")
	}
	root, err := m.secureRoot()
	if err != nil {
		return FileLease{}, err
	}
	jobDir := filepath.Join(root, safeJobDir(jobID))
	if err := secureMkdir(jobDir); err != nil {
		return FileLease{}, err
	}
	path := filepath.Join(jobDir, "issue.token")
	if err := atomicSecretFile(path, []byte(strings.TrimSpace(token)+"\n")); err != nil {
		return FileLease{}, err
	}
	return FileLease{HostPath: path, SandboxPath: IssueTokenSandboxPath}, nil
}

func invalidToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 1<<20 {
		return true
	}
	for _, char := range token {
		if char < 0x21 || char == 0x7f {
			return true
		}
	}
	return false
}

func (m Materializer) Revoke(jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return errors.New("credential materializer: job id is required")
	}
	root, err := m.secureRoot()
	if err != nil {
		return err
	}
	path := filepath.Join(root, safeJobDir(jobID))
	if filepath.Dir(path) != root {
		return errors.New("credential materializer: path escaped root")
	}
	return os.RemoveAll(path)
}

func (m Materializer) secureRoot() (string, error) {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		return "", errors.New("credential materializer: root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Lstat(abs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("credential materializer: root must not be a symbolic link")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	abs, err = canonicalizeParent(abs)
	if err != nil {
		return "", err
	}
	if err := secureMkdir(abs); err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func canonicalizeParent(path string) (string, error) {
	base := filepath.Base(path)
	parent := filepath.Dir(path)
	var missing []string
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			return "", errors.New("credential materializer: no existing root ancestor")
		}
		parent = next
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		canonical = filepath.Join(canonical, missing[index])
	}
	return filepath.Join(canonical, base), nil
}

func secureMkdir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("credential materializer: %s is not a real directory", path)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func atomicSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := secureMkdir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := verifyRegularPrivateFile(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := verifyRegularPrivateFile(path); err != nil {
		_ = os.Remove(path)
		return err
	}
	ok = true
	return nil
}

func verifyRegularPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("credential materializer: insecure credential file %s", path)
	}
	if !singleLink(info) {
		return fmt.Errorf("credential materializer: credential file %s is hard-linked", path)
	}
	return nil
}

func safeJobDir(jobID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(jobID)))
	return "job-" + hex.EncodeToString(digest[:12])
}
