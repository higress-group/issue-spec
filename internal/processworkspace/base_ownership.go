package processworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrAmbiguousOwnership = errors.New("bare write ownership resolves to tracked Git tree")

// validateBaseWriteOwnership rejects bare declarations that name a directory
// in the exact reserved base tree. It deliberately asks Git object storage
// rather than the host filesystem, where a checkout or symlink may describe a
// different shape. Recursive declarations are already explicit and need no
// classification.
func (m *Manager) validateBaseWriteOwnership(ctx context.Context, lease PortableLease) error {
	if lease.Mode != ModeWritable {
		return nil
	}
	for _, declaration := range lease.WriteOwnership {
		if strings.HasSuffix(declaration, "/**") {
			continue
		}
		kind, found, err := m.basePathObjectType(ctx, lease.BaseSHA, declaration)
		if err != nil {
			return fmt.Errorf("classify PROCESS %s write-ownership declaration %q at base %s: %w",
				lease.ProcessID, declaration, lease.BaseSHA, err)
		}
		if found && kind == "tree" {
			return fmt.Errorf("%w: PROCESS %s write-ownership declaration %q resolves to a tracked tree at base %s; declare %q for recursive ownership",
				ErrAmbiguousOwnership, lease.ProcessID, declaration, lease.BaseSHA, declaration+"/**")
		}
	}
	return nil
}

func (m *Manager) basePathObjectType(ctx context.Context, base, declaration string) (string, bool, error) {
	result, err := m.git(ctx, "classify exact base ownership path", m.IntegrationRoot,
		"--literal-pathspecs", "ls-tree", "-z", "--full-tree", base, "--", declaration)
	if err != nil {
		return "", false, err
	}
	if len(result.Stdout) == 0 {
		return "", false, nil
	}
	if result.Stdout[len(result.Stdout)-1] != 0 {
		return "", false, errors.New("Git ls-tree output was not NUL terminated")
	}
	records := bytes.Split(result.Stdout[:len(result.Stdout)-1], []byte{0})
	if len(records) != 1 {
		return "", false, fmt.Errorf("Git ls-tree returned %d records for exact path", len(records))
	}
	metadata, objectPath, ok := bytes.Cut(records[0], []byte{'\t'})
	if !ok || !bytes.Equal(objectPath, []byte(declaration)) {
		return "", false, errors.New("Git ls-tree returned an unexpected exact path record")
	}
	fields := bytes.Fields(metadata)
	if len(fields) != 3 || len(fields[1]) == 0 {
		return "", false, errors.New("Git ls-tree returned malformed object metadata")
	}
	return string(fields[1]), true, nil
}
