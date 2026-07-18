package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/buildinfo"
)

func TestVersionTextAndShorthand(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var out, errOut bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errOut); code != 0 {
			t.Fatalf("Execute(%v) code=%d stderr=%s", args, code, errOut.String())
		}
		for _, want := range []string{"issue-spec development", "channel=development", "revision=unknown", "requirements-skill=sha256:"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("Execute(%v) output missing %q: %s", args, want, out.String())
			}
		}
	}
}

func TestVersionJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute([]string{"version", "--json"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	var got buildinfo.Info
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "development" || got.Channel != "development" || got.Revision != "unknown" || got.GoVersion == "" || got.OS == "" || got.Arch == "" {
		t.Fatalf("version JSON = %+v", got)
	}
}
