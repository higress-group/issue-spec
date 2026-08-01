package assignment

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/durable"
)

const DurableSpecTestID = "issue-spec/durable-spec"

type DurableCheckBinding struct {
	Repository       string
	Proposal         int
	BaselineRevision string
	SubjectRevision  string
	RepositoryRoot   string
}

var durableBindingRepository = regexp.MustCompile(`^[^/[:space:]]+/[^/[:space:]]+$`)
var durableBindingRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// DurableCheckSelector returns the one stable ordinary required-test selector
// for repository mode. Mode none deliberately returns no selector.
func DurableCheckSelector(mode durable.Mode, binding DurableCheckBinding) (*TestSelector, error) {
	normalized, err := durable.NormalizeMode(mode)
	if err != nil {
		return nil, err
	}
	if normalized == durable.ModeNone {
		return nil, nil
	}
	binding.Repository = strings.TrimSpace(binding.Repository)
	binding.BaselineRevision = strings.TrimSpace(binding.BaselineRevision)
	binding.SubjectRevision = strings.TrimSpace(binding.SubjectRevision)
	binding.RepositoryRoot = strings.TrimSpace(binding.RepositoryRoot)
	if !durableBindingRepository.MatchString(binding.Repository) || binding.Proposal <= 0 {
		return nil, errors.New("durable check binding requires repository and positive proposal")
	}
	if !durableBindingRevision.MatchString(binding.BaselineRevision) || !durableBindingRevision.MatchString(binding.SubjectRevision) {
		return nil, errors.New("durable check binding requires exact lowercase baseline and subject revisions")
	}
	// Portable assignments bind the repository root as the checkout-relative
	// current directory. Machine-local delivery paths remain outside Assignment.
	if binding.RepositoryRoot != "." {
		return nil, errors.New("durable check binding repository root must be the portable checkout root `.`")
	}
	command := "issue-spec durable-spec check --repo " + binding.Repository +
		" --proposal " + strconv.Itoa(binding.Proposal) +
		" --baseline " + binding.BaselineRevision +
		" --root . --json"
	return &TestSelector{ID: DurableSpecTestID, Command: command, RevisionBinding: &RevisionBinding{
		Source: RevisionBindingSourceSubjectRevision, Argument: RevisionBindingArgumentSubject,
	}}, nil
}

// WithDurableCheck deterministically merges the built-in durable selector into
// a verification assignment without changing project selector identity.
func (p VerificationPayload) WithDurableCheck(mode durable.Mode, binding DurableCheckBinding) (VerificationPayload, error) {
	selector, err := DurableCheckSelector(mode, binding)
	if err != nil {
		return VerificationPayload{}, err
	}
	if selector == nil {
		return p, nil
	}
	if p.SubjectRevision != binding.SubjectRevision {
		return VerificationPayload{}, fmt.Errorf("durable check subject revision must equal verification subject revision %s", p.SubjectRevision)
	}
	return p.WithVerifierPacket(VerifierPacket{RequiredTests: []TestSelector{*selector}})
}
