package commands

import (
	"os"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
)

func TestMain(m *testing.M) {
	_ = os.Setenv(auth.ProfileEnv, auth.DefaultProfileName)
	os.Exit(m.Run())
}
