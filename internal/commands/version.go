package commands

import (
	"context"
	"fmt"

	"github.com/higress-group/issue-spec/internal/buildinfo"
)

func (a *app) runVersion(_ context.Context, args []string) int {
	fs := newFlagSet("version", a.err)
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		a.errorf("version accepts no positional arguments\n")
		return 2
	}
	identity := buildinfo.Current()
	if *jsonOut {
		return a.outputJSON(identity)
	}
	fmt.Fprintf(a.out, "issue-spec %s channel=%s revision=%s requirements-skill=%s\n",
		identity.Version, identity.Channel, identity.Revision, identity.RequirementsSkillContentID)
	return 0
}
