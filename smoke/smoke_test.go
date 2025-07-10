package smoke

import (
	"os"
	"path"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

func Test_S_Smoke(t *testing.T) {
	// This test simply checks the Monitoring component could be deployed.
	// It does not try to reach out a service, which might be a future work.

	pwd, _ := os.Getwd()
	integration.ProgramTest(t, &integration.ProgramTestOptions{
		Quick:       true,
		SkipRefresh: true,
		Dir:         path.Join(pwd, ".."),
		StackName:   stackName(t.Name()),
		Config: map[string]string{
			"cold-extract": "true", // just make sure it could be set
		},
	})
}

func stackName(tname string) (out string) {
	out = tname
	out = strings.TrimPrefix(out, "Test_S_")
	out = strings.ToLower(out)
	return out
}
