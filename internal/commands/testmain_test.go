package commands

import (
	"flag"
	"os"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/testsupport"
)

func TestMain(m *testing.M) {
	_ = os.Setenv(auth.ProfileEnv, auth.DefaultProfileName)
	// flag.Parse populates testing.Short() so the tier reflects the -short flag.
	flag.Parse()
	tier := "full"
	if testing.Short() {
		tier = "fast"
	}
	os.Exit(testsupport.RunMain(m, tier))
}
