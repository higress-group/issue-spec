// Package routeset defines the composable route contract used by server
// features. It deliberately does not own the application's global router.
package routeset

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// Route is one method-specific net/http ServeMux route.
//
// Name is a stable diagnostic identifier. Pattern is the path portion only;
// Method is kept separate so feature packages cannot accidentally register an
// unscoped path handler that accepts every method.
type Route struct {
	Name    string
	Method  string
	Pattern string
	Handler http.Handler
}

// RouteSet is the only routing artifact a feature package exports. The final
// composition owner combines all sets and creates the single application mux.
type RouteSet struct {
	Name   string
	Routes []Route
}

// Policy applies composition-wide restrictions after all feature sets have
// been validated. ForbiddenPatterns are exact; ForbiddenPrefixes reject both
// the prefix itself and any descendant path.
type Policy struct {
	ForbiddenPatterns []string
	ForbiddenPrefixes []string
}

// SelfHostedPolicy documents and enforces the self-hosted compatibility
// boundary. Runner serve is its intake transport; /notifications is not an API
// route on the self-hosted issue server.
func SelfHostedPolicy() Policy {
	return Policy{ForbiddenPrefixes: []string{"/notifications"}}
}

// Validate checks one set without mutating a router.
func (s RouteSet) Validate() error {
	_, err := ComposeWithPolicy(Policy{}, s)
	return err
}

// Compose validates and returns a deterministic route order.
func Compose(sets ...RouteSet) ([]Route, error) {
	return ComposeWithPolicy(Policy{}, sets...)
}

// ComposeWithPolicy validates all sets, detects collisions before any route is
// mounted, applies policy, and returns a deterministic route order.
func ComposeWithPolicy(policy Policy, sets ...RouteSet) ([]Route, error) {
	orderedSets := append([]RouteSet(nil), sets...)
	sort.SliceStable(orderedSets, func(i, j int) bool { return orderedSets[i].Name < orderedSets[j].Name })

	var problems []string
	setNames := make(map[string]struct{}, len(orderedSets))
	var routes []Route
	for _, set := range orderedSets {
		if strings.TrimSpace(set.Name) == "" {
			problems = append(problems, "route set name is required")
		} else if _, exists := setNames[set.Name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate route set name %q", set.Name))
		} else {
			setNames[set.Name] = struct{}{}
		}
		for _, route := range set.Routes {
			if err := validateRoute(route); err != nil {
				problems = append(problems, fmt.Sprintf("route set %q: %v", set.Name, err))
			}
			routes = append(routes, route)
		}
	}

	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Pattern != routes[j].Pattern {
			return routes[i].Pattern < routes[j].Pattern
		}
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Name < routes[j].Name
	})

	keys := make(map[string]string, len(routes))
	names := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Pattern
		if prior, exists := keys[key]; exists {
			problems = append(problems, fmt.Sprintf("route collision %q between %q and %q", key, prior, route.Name))
		} else {
			keys[key] = route.Name
		}
		if route.Name != "" {
			if _, exists := names[route.Name]; exists {
				problems = append(problems, fmt.Sprintf("duplicate route name %q", route.Name))
			} else {
				names[route.Name] = struct{}{}
			}
		}
	}

	forbidden := make(map[string]struct{}, len(policy.ForbiddenPatterns))
	for _, pattern := range policy.ForbiddenPatterns {
		forbidden[pattern] = struct{}{}
	}
	for _, route := range routes {
		if _, denied := forbidden[route.Pattern]; denied {
			problems = append(problems, fmt.Sprintf("route %q uses forbidden pattern %q", route.Name, route.Pattern))
		}
		for _, prefix := range policy.ForbiddenPrefixes {
			if route.Pattern == prefix || strings.HasPrefix(route.Pattern, strings.TrimRight(prefix, "/")+"/") {
				problems = append(problems, fmt.Sprintf("route %q uses forbidden path prefix %q", route.Name, prefix))
			}
		}
	}

	if len(problems) == 0 {
		if err := validateServeMuxCompatibility(routes); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return routes, nil
}

// NewMux validates all routes before returning a fully composed mux.
func NewMux(policy Policy, sets ...RouteSet) (*http.ServeMux, error) {
	routes, err := ComposeWithPolicy(policy, sets...)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	for _, route := range routes {
		mux.Handle(route.Method+" "+route.Pattern, route.Handler)
	}
	return mux, nil
}

func validateRoute(route Route) error {
	if strings.TrimSpace(route.Name) == "" {
		return errors.New("route name is required")
	}
	if route.Method == "" || route.Method != strings.ToUpper(route.Method) || !isToken(route.Method) {
		return fmt.Errorf("route %q has invalid canonical method %q", route.Name, route.Method)
	}
	if route.Pattern == "" || !strings.HasPrefix(route.Pattern, "/") {
		return fmt.Errorf("route %q pattern must start with /", route.Name)
	}
	if strings.ContainsAny(route.Pattern, "?#") {
		return fmt.Errorf("route %q pattern must not contain query or fragment", route.Name)
	}
	if _, err := url.ParseRequestURI(route.Pattern); err != nil {
		return fmt.Errorf("route %q has invalid pattern: %v", route.Name, err)
	}
	if route.Handler == nil {
		return fmt.Errorf("route %q handler is required", route.Name)
	}
	return nil
}

func isToken(value string) bool {
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) || unicode.IsControl(r) {
			return false
		}
	}
	return value != ""
}

func validateServeMuxCompatibility(routes []Route) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("incompatible ServeMux patterns: %v", recovered)
		}
	}()
	mux := http.NewServeMux()
	for _, route := range routes {
		mux.Handle(route.Method+" "+route.Pattern, route.Handler)
	}
	return nil
}
