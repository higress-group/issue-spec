package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestCodeChangeRationaleHelpKeepsDeprecatedSessionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runCodeChange(t.Context(), []string{"rationale", "--help"}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
	for _, flag := range []string{"--repo", "--implement", "--process", "--spec", "--spec-url", "--body", "--agent", "--agent-session", "--json"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help missing %s:\n%s", flag, out.String())
		}
	}
	if !strings.Contains(out.String(), "deprecated compatibility flag; ignored") {
		t.Fatalf("help does not mark --agent-session deprecated:\n%s", out.String())
	}
}

func TestCodeChangeRationaleAppendOnlyExactRetryAndRefresh(t *testing.T) {
	backend := newFakeCodeChangeBackend()
	canonical := "https://code.example/acme/widgets/changes/42"
	backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-abc", 7)}
	processBody := codeChangeRationaleProcessBody(t, canonical)
	comments := []github.Comment{{ID: 17, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-17", Body: processBody}}
	created, updated := 0, 0
	backend.issueBackend = fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		createComment: func(_ context.Context, repo string, issue int, body string) (github.Comment, error) {
			created++
			comment := github.Comment{ID: int64(20 + created), HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-created", Body: body}
			comments = append(comments, comment)
			return comment, nil
		},
		updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			updated++
			for index := range comments {
				if comments[index].ID == id {
					comments[index].Body = body
					return comments[index], nil
				}
			}
			return github.Comment{}, context.Canceled
		},
	}
	app, out, errOut := setupCodeChangeLinkApp(t, backend)
	provider := &fakeRationaleMutationProvider{}
	app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
		return provider, nil
	}
	args := codeChangeRationaleArgs("first rationale")
	if code := app.runCodeChange(t.Context(), args); code != 0 || errOut.Len() != 0 {
		t.Fatalf("create exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result codeChangeRationaleResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if !result.OK || !result.Created || result.PublicationState != model.CodeChangeRationalePublishedExternal ||
		result.ExternalCapability != "available" || result.ExternalCommentID == "" ||
		result.RepresentationVersion != 7 || result.SubjectRevision != "head-abc" ||
		created != 1 || updated != 1 || len(provider.requests) != 1 {
		t.Fatalf("result=%+v created=%d updated=%d requests=%d", result, created, updated, len(provider.requests))
	}
	marker, found, err := model.FindCodeChangeRationaleMarker(comments[len(comments)-1].Body)
	if err != nil || !found || model.CodeChangeRationaleVersion(marker) != 2 ||
		marker.Publication == nil || marker.Publication.State != model.CodeChangeRationalePublishedExternal ||
		marker.Agent != "PROCESS-007 worker" || marker.AgentSessionID != "" ||
		marker.ReferenceVersion != 7 || marker.SubjectRevision != "head-abc" {
		t.Fatalf("marker=%+v found=%v err=%v", marker, found, err)
	}
	request := provider.requests[0]
	if request.HeadRevision != "head-abc" || request.Reference.ChangeID != "change-42" ||
		request.Metadata["kind"] != "rationale" || request.Metadata["rationale_id"] != marker.RationaleID ||
		strings.Contains(request.Body, "published_external") || strings.Contains(request.Body, marker.Publication.ExternalID) {
		t.Fatalf("mutation request=%+v", request)
	}

	out.Reset()
	if code := app.runCodeChange(t.Context(), args); code != 0 || created != 1 || updated != 1 || len(provider.requests) != 1 {
		t.Fatalf("exact retry exit=%d created=%d updated=%d requests=%d stderr=%q",
			code, created, updated, len(provider.requests), errOut.String())
	}
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Created || !result.Already {
		t.Fatalf("exact retry unexpectedly created: %+v", result)
	}

	// Different prose is a different logical identity and receives its own
	// recoverable carrier and provider request.
	out.Reset()
	if code := app.runCodeChange(t.Context(), codeChangeRationaleArgs("materially different rationale")); code != 0 ||
		created != 2 || updated != 2 || len(provider.requests) != 2 {
		t.Fatalf("non-exact retry exit=%d created=%d updated=%d requests=%d stderr=%q",
			code, created, updated, len(provider.requests), errOut.String())
	}

	backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-def", 8)}
	out.Reset()
	if code := app.runCodeChange(t.Context(), codeChangeRationaleArgs("refreshed head rationale")); code != 0 ||
		created != 3 || updated != 3 || len(provider.requests) != 3 {
		t.Fatalf("refresh exit=%d created=%d updated=%d requests=%d stderr=%q",
			code, created, updated, len(provider.requests), errOut.String())
	}
	latest, found, err := model.FindCodeChangeRationaleMarker(comments[len(comments)-1].Body)
	if err != nil || !found || latest.ReferenceVersion != 8 || latest.SubjectRevision != "head-def" ||
		latest.Publication.State != model.CodeChangeRationalePublishedExternal {
		t.Fatalf("latest=%+v found=%v err=%v", latest, found, err)
	}
	if old, _, _ := model.FindCodeChangeRationaleMarker(comments[1].Body); old.ReferenceVersion != 7 || old.SubjectRevision != "head-abc" {
		t.Fatalf("old append-only marker was changed: %+v", old)
	}
}

func TestCodeChangeRationalePreUpgradeRetryIgnoresLegacySessionMetadata(t *testing.T) {
	backend := newFakeCodeChangeBackend()
	canonical := "https://code.example/acme/widgets/changes/42"
	backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-abc", 7)}
	legacyMarker := model.CodeChangeRationaleMarker{Process: "PROCESS-001", Spec: "SPEC-001",
		SpecURL: "https://issues.test/acme/widgets/issues/1#issuecomment-2", ProviderKey: "code.example",
		ExternalRepository: "acme/widgets-code", ChangeID: "change-42", ReferenceVersion: 7,
		SubjectRevision: "head-abc", Agent: "PROCESS-007 worker", AgentSessionID: "worker-session",
		AgentSessionSource: "CODEX_THREAD_ID"}
	legacyBody, err := model.RenderCodeChangeRationaleBody(legacyMarker, "first rationale")
	if err != nil {
		t.Fatal(err)
	}
	legacyBody = strings.Replace(legacyBody, "Agent Session Source: CODEX_THREAD_ID",
		"Agent Session Source: stale-visible-source", 1)
	comments := []github.Comment{
		{ID: 17, Body: codeChangeRationaleProcessBody(t, canonical)},
		{ID: 18, HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-18", Body: legacyBody},
	}
	created := 0
	backend.issueBackend = fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			created++
			return github.Comment{ID: 19, Body: body}, nil
		},
	}
	app, out, errOut := setupCodeChangeLinkApp(t, backend)
	if code := app.runCodeChange(t.Context(), codeChangeRationaleArgs("first rationale")); code != 0 || created != 0 {
		t.Fatalf("pre-upgrade retry exit=%d created=%d stdout=%q stderr=%q", code, created, out.String(), errOut.String())
	}
	var result codeChangeRationaleResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if !result.OK || result.Created || result.CommentID != 18 {
		t.Fatalf("pre-upgrade retry result=%+v", result)
	}
}

func TestCodeChangeRationaleRejectsMissingProcessSpecAndChangeLinks(t *testing.T) {
	canonical := "https://code.example/acme/widgets/changes/42"
	tests := map[string]struct {
		body string
		want string
	}{
		"wrong spec":     {body: codeChangeRationaleProcessBody(t, canonical), want: "does not cover"},
		"missing change": {body: strings.Replace(codeChangeRationaleProcessBody(t, canonical), canonical, "N/A", 1), want: "does not link"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			backend := newFakeCodeChangeBackend()
			backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-abc", 7)}
			backend.issueBackend = fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
				return []github.Comment{{ID: 17, Body: test.body}}, nil
			}}
			app, out, errOut := setupCodeChangeLinkApp(t, backend)
			args := codeChangeRationaleArgs("why")
			if name == "wrong spec" {
				for i := range args {
					if args[i] == "SPEC-001" {
						args[i] = "SPEC-002"
					}
				}
			}
			if code := app.runCodeChange(t.Context(), args); code != 1 || !strings.Contains(out.String()+errOut.String(), test.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestUniqueActiveCodeChangeIdentityRejectsTrailingMetadata(t *testing.T) {
	reference := codeChangeRationaleReference("https://code.example/acme/widgets/changes/42", "head-abc", 7)
	reference.Metadata = append(reference.Metadata, []byte(` {"head_revision":"shadow"}`)...)
	if _, _, err := uniqueActiveCodeChangeIdentity([]github.NativeReference{reference}); err == nil {
		t.Fatal("trailing reference metadata was accepted")
	}
}

func TestCodeChangeRationaleCapabilityFallbackAndBrokenAdvertisedMutation(t *testing.T) {
	t.Run("missing capability", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{
			capabilities: []codereview.Capability{codereview.CapabilityEvidenceSnapshot},
		}
		harness := newRationaleHarness(t, provider)
		if code := harness.run(t, "fallback rationale"); code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, harness.out.String(), harness.errOut.String())
		}
		var result codeChangeRationaleResult
		decodeCommandJSON(t, harness.out.Bytes(), &result)
		if result.PublicationState != model.CodeChangeRationaleExternalUnavailable ||
			result.ExternalCapability != "unavailable" || harness.creates != 1 || harness.updates != 0 ||
			len(provider.requests) != 0 {
			t.Fatalf("result=%+v creates=%d updates=%d requests=%d",
				result, harness.creates, harness.updates, len(provider.requests))
		}
		marker, found, err := model.FindCodeChangeRationaleMarker(harness.comments[len(harness.comments)-1].Body)
		if err != nil || !found || !model.CodeChangeRationaleGateEligible(marker) {
			t.Fatalf("fallback marker=%+v found=%v err=%v", marker, found, err)
		}
		harness.reset()
		if code := harness.run(t, "fallback rationale"); code != 0 || harness.creates != 1 {
			t.Fatalf("retry exit=%d creates=%d stdout=%q", code, harness.creates, harness.out.String())
		}
	})

	t.Run("advertised without mutation", func(t *testing.T) {
		provider := commandReadOnlyProvider{capabilities: []codereview.Capability{codereview.CapabilityChangeComment}}
		harness := newRationaleHarness(t, provider)
		if code := harness.run(t, "must fail"); code != 1 || harness.creates != 0 ||
			!strings.Contains(harness.out.String(), "advertised but mutations are not implemented") {
			t.Fatalf("exit=%d creates=%d stdout=%q stderr=%q",
				code, harness.creates, harness.out.String(), harness.errOut.String())
		}
	})

	t.Run("missing capability target moves during discovery", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{
			capabilities: []codereview.Capability{codereview.CapabilityEvidenceSnapshot},
		}
		harness := newRationaleHarness(t, provider)
		provider.capabilitiesHook = func() {
			harness.backend.references = []github.NativeReference{codeChangeRationaleReference(
				"https://code.example/acme/widgets/changes/42", "head-moved", 8)}
		}
		if code := harness.run(t, "fallback must remain exact"); code != 1 ||
			harness.creates != 0 || harness.updates != 0 || len(provider.requests) != 0 ||
			!strings.Contains(harness.out.String(), "before carrier creation") {
			t.Fatalf("exit=%d creates=%d updates=%d requests=%d stdout=%q stderr=%q",
				code, harness.creates, harness.updates, len(provider.requests),
				harness.out.String(), harness.errOut.String())
		}
	})
}

func TestCodeChangeRationaleProviderAndCarrierFailuresConverge(t *testing.T) {
	t.Run("lost provider acknowledgement", func(t *testing.T) {
		calls := 0
		provider := &fakeRationaleMutationProvider{}
		provider.mutate = func(request codereview.MutationRequest) (codereview.MutationResult, error) {
			calls++
			if calls == 1 {
				return codereview.MutationResult{}, errors.New("provider response lost")
			}
			return codereview.MutationResult{Reference: request.Reference, ExternalID: "comment-stable",
				CanonicalURL: "https://code.example/acme/widgets/comments/stable"}, nil
		}
		harness := newRationaleHarness(t, provider)
		if code := harness.run(t, "recover provider"); code != 1 || harness.creates != 1 || harness.updates != 0 {
			t.Fatalf("first exit=%d creates=%d updates=%d stdout=%q", code, harness.creates, harness.updates, harness.out.String())
		}
		pending, found, err := model.FindCodeChangeRationaleMarker(harness.comments[len(harness.comments)-1].Body)
		if err != nil || !found || pending.Publication.State != model.CodeChangeRationalePendingExternal ||
			model.CodeChangeRationaleGateEligible(pending) {
			t.Fatalf("pending=%+v found=%v err=%v", pending, found, err)
		}
		harness.reset()
		if code := harness.run(t, "recover provider"); code != 0 || harness.creates != 1 ||
			harness.updates != 1 || calls != 2 {
			t.Fatalf("retry exit=%d creates=%d updates=%d calls=%d stdout=%q stderr=%q",
				code, harness.creates, harness.updates, calls, harness.out.String(), harness.errOut.String())
		}
	})

	t.Run("failed issue update", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		attempts := 0
		harness.update = func(id int64, body string) (github.Comment, error) {
			attempts++
			if attempts == 1 {
				return github.Comment{}, errors.New("issue update failed")
			}
			return harness.storeUpdate(id, body)
		}
		if code := harness.run(t, "recover issue update"); code != 1 {
			t.Fatalf("first exit=%d stdout=%q", code, harness.out.String())
		}
		harness.reset()
		if code := harness.run(t, "recover issue update"); code != 0 ||
			harness.creates != 1 || harness.updates != 1 || len(provider.requests) != 2 {
			t.Fatalf("retry exit=%d creates=%d updates=%d requests=%d stdout=%q",
				code, harness.creates, harness.updates, len(provider.requests), harness.out.String())
		}
	})

	t.Run("lost issue update acknowledgement", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		attempts := 0
		harness.update = func(id int64, body string) (github.Comment, error) {
			attempts++
			comment, err := harness.storeUpdate(id, body)
			if err != nil {
				return github.Comment{}, err
			}
			if attempts == 1 {
				return github.Comment{}, errors.New("issue update response lost")
			}
			return comment, nil
		}
		if code := harness.run(t, "lost update response"); code != 1 {
			t.Fatalf("first exit=%d stdout=%q", code, harness.out.String())
		}
		harness.reset()
		if code := harness.run(t, "lost update response"); code != 0 ||
			len(provider.requests) != 1 || harness.updates != 1 {
			t.Fatalf("retry exit=%d requests=%d updates=%d stdout=%q",
				code, len(provider.requests), harness.updates, harness.out.String())
		}
	})

	t.Run("lost create acknowledgement", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		attempts := 0
		harness.create = func(body string) (github.Comment, error) {
			attempts++
			comment := harness.storeCreate(body)
			if attempts == 1 {
				return github.Comment{}, errors.New("issue create response lost")
			}
			return comment, nil
		}
		if code := harness.run(t, "lost create response"); code != 1 {
			t.Fatalf("first exit=%d stdout=%q", code, harness.out.String())
		}
		harness.reset()
		if code := harness.run(t, "lost create response"); code != 0 ||
			harness.creates != 1 || harness.updates != 1 || len(provider.requests) != 1 {
			t.Fatalf("retry exit=%d creates=%d updates=%d requests=%d stdout=%q",
				code, harness.creates, harness.updates, len(provider.requests), harness.out.String())
		}
	})
}

func TestCodeChangeRationaleRejectsAmbiguousMalformedAndMovedState(t *testing.T) {
	t.Run("duplicate carrier", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		base := model.CodeChangeRationaleMarker{Process: "PROCESS-001", Spec: "SPEC-001",
			SpecURL: "https://issues.test/acme/widgets/issues/1#issuecomment-2", ProviderKey: "code.example",
			ExternalRepository: "acme/widgets-code", ChangeID: "change-42", ReferenceVersion: 7,
			SubjectRevision: "head-abc", Agent: "PROCESS-007 worker"}
		pending, err := model.PrepareCodeChangeRationaleMarker(base, "duplicate", model.CodeChangeRationalePendingExternal, "", "")
		if err != nil {
			t.Fatal(err)
		}
		body, err := model.RenderCodeChangeRationaleBody(pending, "duplicate")
		if err != nil {
			t.Fatal(err)
		}
		harness.comments = append(harness.comments,
			github.Comment{ID: 21, Body: body}, github.Comment{ID: 22, Body: body})
		if code := harness.run(t, "duplicate"); code != 1 || len(provider.requests) != 0 ||
			!strings.Contains(harness.out.String(), "has 2 carriers") {
			t.Fatalf("exit=%d requests=%d stdout=%q", code, len(provider.requests), harness.out.String())
		}
	})

	t.Run("malformed carrier", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		harness.comments = append(harness.comments, github.Comment{ID: 21,
			Body: "<!-- issue-spec:code-change-rationale payload=% version=2 -->\n"})
		if code := harness.run(t, "malformed"); code != 1 || harness.creates != 0 ||
			len(provider.requests) != 0 {
			t.Fatalf("exit=%d creates=%d requests=%d stdout=%q",
				code, harness.creates, len(provider.requests), harness.out.String())
		}
	})

	t.Run("target moves before provider", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		harness.create = func(body string) (github.Comment, error) {
			comment := harness.storeCreate(body)
			harness.backend.references = []github.NativeReference{codeChangeRationaleReference(
				"https://code.example/acme/widgets/changes/42", "head-moved", 8)}
			return comment, nil
		}
		if code := harness.run(t, "move before"); code != 1 || len(provider.requests) != 0 ||
			harness.updates != 0 {
			t.Fatalf("exit=%d requests=%d updates=%d stdout=%q",
				code, len(provider.requests), harness.updates, harness.out.String())
		}
	})

	t.Run("target moves after provider", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		harness := newRationaleHarness(t, provider)
		provider.mutate = func(request codereview.MutationRequest) (codereview.MutationResult, error) {
			harness.backend.references = []github.NativeReference{codeChangeRationaleReference(
				"https://code.example/acme/widgets/changes/42", "head-moved", 8)}
			return codereview.MutationResult{Reference: request.Reference, ExternalID: "comment-1",
				CanonicalURL: "https://code.example/acme/widgets/comments/1"}, nil
		}
		if code := harness.run(t, "move after"); code != 1 || len(provider.requests) != 1 ||
			harness.updates != 0 {
			t.Fatalf("exit=%d requests=%d updates=%d stdout=%q",
				code, len(provider.requests), harness.updates, harness.out.String())
		}
	})

	t.Run("malformed provider result", func(t *testing.T) {
		provider := &fakeRationaleMutationProvider{}
		provider.mutate = func(request codereview.MutationRequest) (codereview.MutationResult, error) {
			request.Reference.ChangeID = "other"
			return codereview.MutationResult{Reference: request.Reference, ExternalID: "comment-1",
				CanonicalURL: "https://code.example/acme/widgets/comments/1"}, nil
		}
		harness := newRationaleHarness(t, provider)
		if code := harness.run(t, "bad provider"); code != 1 || harness.updates != 0 {
			t.Fatalf("exit=%d updates=%d stdout=%q", code, harness.updates, harness.out.String())
		}
	})
}

type rationaleHarness struct {
	backend  *fakeCodeChangeBackend
	app      *app
	out      *bytes.Buffer
	errOut   *bytes.Buffer
	comments []github.Comment
	creates  int
	updates  int
	create   func(string) (github.Comment, error)
	update   func(int64, string) (github.Comment, error)
}

func newRationaleHarness(t *testing.T, provider codereview.Provider) *rationaleHarness {
	t.Helper()
	harness := &rationaleHarness{backend: newFakeCodeChangeBackend()}
	canonical := "https://code.example/acme/widgets/changes/42"
	harness.backend.references = []github.NativeReference{codeChangeRationaleReference(canonical, "head-abc", 7)}
	harness.comments = []github.Comment{{ID: 17, Body: codeChangeRationaleProcessBody(t, canonical)}}
	harness.backend.issueBackend = fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), harness.comments...), nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			if harness.create != nil {
				return harness.create(body)
			}
			return harness.storeCreate(body), nil
		},
		updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			if harness.update != nil {
				return harness.update(id, body)
			}
			return harness.storeUpdate(id, body)
		},
	}
	harness.app, harness.out, harness.errOut = setupCodeChangeLinkApp(t, harness.backend)
	harness.app.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) {
		return provider, nil
	}
	return harness
}

func (h *rationaleHarness) storeCreate(body string) github.Comment {
	h.creates++
	comment := github.Comment{ID: int64(20 + h.creates),
		HTMLURL: "https://issues.test/acme/widgets/issues/9#issuecomment-created", Body: body}
	h.comments = append(h.comments, comment)
	return comment
}

func (h *rationaleHarness) storeUpdate(id int64, body string) (github.Comment, error) {
	for index := range h.comments {
		if h.comments[index].ID == id {
			h.updates++
			h.comments[index].Body = body
			return h.comments[index], nil
		}
	}
	return github.Comment{}, errors.New("comment not found")
}

func (h *rationaleHarness) run(t *testing.T, body string) int {
	t.Helper()
	return h.app.runCodeChange(t.Context(), codeChangeRationaleArgs(body))
}

func (h *rationaleHarness) reset() {
	h.out.Reset()
	h.errOut.Reset()
}

type fakeRationaleMutationProvider struct {
	capabilities     []codereview.Capability
	capabilitiesHook func()
	requests         []codereview.MutationRequest
	mutate           func(codereview.MutationRequest) (codereview.MutationResult, error)
}

func (p *fakeRationaleMutationProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	if p.capabilitiesHook != nil {
		p.capabilitiesHook()
	}
	values := p.capabilities
	if values == nil {
		values = []codereview.Capability{codereview.CapabilityChangeComment}
	}
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion, Values: values}, nil
}

func (p *fakeRationaleMutationProvider) Snapshot(context.Context, codereview.SnapshotRequest) (codereview.Snapshot, error) {
	return codereview.Snapshot{}, nil
}

func (p *fakeRationaleMutationProvider) Mutate(_ context.Context, request codereview.MutationRequest) (codereview.MutationResult, error) {
	p.requests = append(p.requests, request)
	if p.mutate != nil {
		return p.mutate(request)
	}
	return codereview.MutationResult{Reference: request.Reference,
		ExternalID:   "comment-" + request.Metadata["rationale_id"].(string),
		CanonicalURL: "https://code.example/acme/widgets/comments/17"}, nil
}

func codeChangeRationaleArgs(body string) []string {
	return []string{"rationale", "--repo", "acme/widgets", "--implement", "9", "--process", "PROCESS-001",
		"--spec", "SPEC-001", "--spec-url", "https://issues.test/acme/widgets/issues/1#issuecomment-2",
		"--body", body, "--agent", "PROCESS-007 worker", "--json"}
}

func codeChangeRationaleReference(canonical, revision string, version int64) github.NativeReference {
	metadata, _ := json.Marshal(map[string]string{"head_revision": revision})
	return github.NativeReference{ID: "reference-1", ProviderKey: "code.example", RelationKind: "code_change",
		ExternalRepositoryID: "acme/widgets-code", ExternalID: "change-42", CanonicalURL: canonical,
		LifecycleState: "active", Metadata: metadata, RepresentationVersion: version}
}

func codeChangeRationaleProcessBody(t *testing.T, canonical string) string {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", `## Process: implement provider flow

### Parent TASK

- TASK-001

### Execution Class

- change-bearing

### Covers

- SPEC-001

### Handoff

Implementation complete.`, model.BodyOptions{Agent: "Coordinator", AgentSessionID: "coordinator-session",
		AgentSessionSource: "CODEX_THREAD_ID", Status: "done", Scope: "provider",
		Links: map[string][]string{"Related Comments": {"https://issues.test/acme/widgets/issues/1#issuecomment-2"}, "PR": {canonical}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
