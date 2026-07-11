// Package meta exposes non-sensitive server capability discovery. The final
// composition owner injects flags from the RouteSets it actually mounts.
package meta

import (
	"net/http"

	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
)

type Features struct {
	Bootstrap            bool `json:"bootstrap"`
	PersonalAccessTokens bool `json:"personal_access_tokens"`
	Organizations        bool `json:"organizations"`
	SourceBindings       bool `json:"source_bindings"`
	Webhooks             bool `json:"webhooks"`
	ChangeBoards         bool `json:"change_boards"`
	Runner               bool `json:"runner"`
	RecoveryExchange     bool `json:"recovery_exchange"`
}

type Dependencies struct{ Features Features }

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	h := handlers{features: deps.Features}
	set := routeset.RouteSet{Name: "native-meta", Routes: []routeset.Route{{
		Name: "native.meta.get", Method: http.MethodGet, Pattern: "/api/v1/meta",
		Handler: adminapi.WithRequestID(http.HandlerFunc(h.get)),
	}}}
	return set, set.Validate()
}

type handlers struct{ features Features }

func (h handlers) get(w http.ResponseWriter, _ *http.Request) {
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"api_version": "v1", "features": h.features})
}
