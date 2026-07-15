package publicurl

import "testing"

func TestRepositoryResourcePathsKeepCanonicalWebAndAPIShapes(t *testing.T) {
	paths := RepositoryResource("Ingress Team", "istio/gateway")
	checks := map[string]string{
		"repository API": paths.API(),
		"repository Web": paths.Web(),
		"issues API":     paths.IssuesAPI(),
		"issues Web":     paths.IssuesWeb(),
		"issue API":      paths.IssueAPI(21),
		"issue Web":      paths.IssueWeb(21),
		"comment API":    paths.CommentAPI(42),
	}
	wants := map[string]string{
		"repository API": "/repos/Ingress%20Team/istio%2Fgateway",
		"repository Web": "/Ingress%20Team/istio%2Fgateway",
		"issues API":     "/repos/Ingress%20Team/istio%2Fgateway/issues",
		"issues Web":     "/Ingress%20Team/istio%2Fgateway/issues",
		"issue API":      "/repos/Ingress%20Team/istio%2Fgateway/issues/21",
		"issue Web":      "/Ingress%20Team/istio%2Fgateway/issues/21",
		"comment API":    "/repos/Ingress%20Team/istio%2Fgateway/issues/comments/42",
	}
	for name, got := range checks {
		if got != wants[name] {
			t.Errorf("%s = %q, want %q", name, got, wants[name])
		}
	}
}
