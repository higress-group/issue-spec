package comments

import (
	"testing"

	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
)

func TestRouteSetRequiresServiceAndUsesOneUnambiguousTailPerMethod(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("nil service was accepted")
	}
	set, err := NewRouteSet(Dependencies{Service: &issueapi.Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Routes) != 4 {
		t.Fatalf("routes = %d, want one GET/POST/PATCH/DELETE tail dispatcher", len(set.Routes))
	}
}
