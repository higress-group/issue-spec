package assignment

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

const revisionCommandSuffix = " --subject "

var (
	fullRevisionPattern      = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	directCommandWordPattern = regexp.MustCompile(`^[A-Za-z0-9_./-][A-Za-z0-9_./:@+,=-]*$`)
)

// ResolveTestSelector deterministically expands a valid bound selector after
// its full authoritative object ID is known. Existing command bytes are
// preserved; the resolver appends exactly one typed argument/value pair.
func ResolveTestSelector(selector TestSelector, revision string) (ResolvedTestIdentity, error) {
	if err := validateRequiredText("test_selector.id", selector.ID, maxIDLength); err != nil {
		return ResolvedTestIdentity{}, err
	}
	if err := validateRequiredText("test_selector.command", selector.Command, maxCommandLength); err != nil {
		return ResolvedTestIdentity{}, err
	}
	if selector.RevisionBinding == nil {
		return ResolvedTestIdentity{}, errors.New("test_selector.revision_binding: is required for resolution")
	}
	if err := validateBoundTestSelector("test_selector", selector); err != nil {
		return ResolvedTestIdentity{}, err
	}
	if !fullRevisionPattern.MatchString(revision) {
		return ResolvedTestIdentity{}, errors.New("resolved_revision: must be an exact lowercase full Git object ID")
	}
	command := selector.Command + revisionCommandSuffix + revision
	if len(command) > maxCommandLength {
		return ResolvedTestIdentity{}, fmt.Errorf("resolved command: exceeds %d bytes", maxCommandLength)
	}
	return ResolvedTestIdentity{
		AssignedSelector: cloneTestSelector(selector),
		ResolvedRevision: revision,
		Command:          command,
	}, nil
}

// ValidateTestSelectorRevisionContract validates a selector against the role
// and its authoritative revision. Implementation may omit the revision only
// for a result-revision binding whose result commit does not exist yet.
// Review and verification always provide their immutable subject revision.
func ValidateTestSelectorRevisionContract(role Role, authoritativeRevision string, selector TestSelector) error {
	if !validRole(role) {
		return fmt.Errorf("role: unsupported value %q", role)
	}
	if err := validateRequiredText("test_selector.id", selector.ID, maxIDLength); err != nil {
		return err
	}
	if err := validateRequiredText("test_selector.command", selector.Command, maxCommandLength); err != nil {
		return err
	}
	if selector.RevisionBinding != nil {
		if err := validateBoundTestSelector("test_selector", selector); err != nil {
			return err
		}
		expectedSource := RevisionBindingSourceSubjectRevision
		if role == RoleImplementation {
			expectedSource = RevisionBindingSourceResultRevision
		}
		if selector.RevisionBinding.Source != expectedSource {
			return fmt.Errorf("test_selector.revision_binding.source: %q is not supported for %s tests", selector.RevisionBinding.Source, role)
		}
		if authoritativeRevision == "" && role == RoleImplementation {
			return nil
		}
		_, err := ResolveTestSelector(selector, authoritativeRevision)
		return err
	}

	literalRevision, sensitive, err := literalSubjectRevision(selector.Command)
	if err != nil {
		return fmt.Errorf("test_selector.command: %w", err)
	}
	if !sensitive {
		return nil
	}
	if !fullRevisionPattern.MatchString(authoritativeRevision) {
		return errors.New("authoritative_revision: must be an exact lowercase full Git object ID for a revision-sensitive literal selector")
	}
	if literalRevision != authoritativeRevision {
		return errors.New("test_selector.command: literal --subject must equal the authoritative revision")
	}
	return nil
}

func validateBoundTestSelector(name string, selector TestSelector) error {
	binding := selector.RevisionBinding
	if binding == nil {
		return nil
	}
	if binding.Source != RevisionBindingSourceResultRevision && binding.Source != RevisionBindingSourceSubjectRevision {
		return fmt.Errorf("%s.revision_binding.source: unsupported value %q", name, binding.Source)
	}
	if binding.Argument != RevisionBindingArgumentSubject {
		return fmt.Errorf("%s.revision_binding.argument: unsupported value %q", name, binding.Argument)
	}
	if len(selector.Command)+len(revisionCommandSuffix)+64 > maxCommandLength {
		return fmt.Errorf("%s.command: cannot fit a full resolved revision within %d bytes", name, maxCommandLength)
	}

	nonEmptyWords, err := directCommandWords(selector.Command)
	if err != nil {
		return fmt.Errorf("%s.command: %w", name, err)
	}
	for _, word := range nonEmptyWords {
		if word == string(binding.Argument) || strings.HasPrefix(word, string(binding.Argument)+"=") {
			return fmt.Errorf("%s.command: already contains bound argument %s", name, binding.Argument)
		}
	}
	return nil
}

func literalSubjectRevision(command string) (string, bool, error) {
	rawWords := strings.Fields(command)
	rawDurableSignature := len(rawWords) >= 3 && rawWords[0] == "issue-spec" && rawWords[1] == "durable-spec" && rawWords[2] == "check"
	words, err := directCommandWords(command)
	if err != nil {
		if rawDurableSignature {
			return "", true, err
		}
		// Version 1 does not interpret arbitrary shell programs. Existing opaque
		// literal selectors outside the built-in direct signature stay literal.
		return "", false, nil
	}
	var revision string
	for i := 0; i < len(words); i++ {
		word := words[i]
		if strings.HasPrefix(word, string(RevisionBindingArgumentSubject)+"=") {
			return "", true, errors.New("literal revision contract requires separate --subject and revision words")
		}
		if word != string(RevisionBindingArgumentSubject) {
			continue
		}
		if revision != "" {
			return "", true, errors.New("literal revision contract contains duplicate --subject arguments")
		}
		if i+1 >= len(words) {
			return "", true, errors.New("literal revision contract is missing the --subject revision")
		}
		revision = words[i+1]
		i++
	}
	if revision == "" {
		if rawDurableSignature {
			return "", true, errors.New("recognized issue-spec durable-spec check requires an exact --subject revision or typed binding")
		}
		return "", false, nil
	}
	if !fullRevisionPattern.MatchString(revision) {
		return "", true, errors.New("literal --subject must be an exact lowercase full Git object ID")
	}
	return revision, true, nil
}

func directCommandWords(command string) ([]string, error) {
	words := strings.Split(command, " ")
	nonEmptyWords := words[:0]
	for _, word := range words {
		if word == "" {
			continue
		}
		if !directCommandWordPattern.MatchString(word) {
			return nil, errors.New("revision-bound selectors require conservative direct-command words")
		}
		nonEmptyWords = append(nonEmptyWords, word)
	}
	if len(nonEmptyWords) == 0 {
		return nil, errors.New("direct command requires an executable")
	}
	for _, word := range nonEmptyWords {
		if isShellExecutable(word) {
			return nil, errors.New("shell wrappers are not supported for revision-bound selectors")
		}
	}
	return nonEmptyWords, nil
}

func isShellExecutable(word string) bool {
	switch path.Base(word) {
	case "sh", "bash", "dash", "zsh", "ksh", "fish":
		return true
	default:
		return false
	}
}

func cloneTestSelector(value TestSelector) TestSelector {
	clone := value
	if value.RevisionBinding != nil {
		binding := *value.RevisionBinding
		clone.RevisionBinding = &binding
	}
	return clone
}

func cloneTestSelectors(values []TestSelector) []TestSelector {
	clones := make([]TestSelector, len(values))
	for i := range values {
		clones[i] = cloneTestSelector(values[i])
	}
	return clones
}

// TestSelectorIdentityEqual compares the complete declarative selector
// identity, including an optional revision binding.
func TestSelectorIdentityEqual(left, right TestSelector) bool {
	if left.ID != right.ID || left.Command != right.Command {
		return false
	}
	if left.RevisionBinding == nil || right.RevisionBinding == nil {
		return left.RevisionBinding == nil && right.RevisionBinding == nil
	}
	return *left.RevisionBinding == *right.RevisionBinding
}
