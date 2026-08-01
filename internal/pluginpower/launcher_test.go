package pluginpower

import (
	"strings"
	"testing"
)

func TestSupervisorEnvironmentExcludesUnreviewedSecrets(t *testing.T) {
	t.Setenv("PATH", "test-path")
	t.Setenv("DONETHEN_TEST_SECRET", "must-not-be-inherited")
	t.Setenv("OPENAI_API_KEY", "must-not-be-inherited")
	environment := supervisorEnvironment()
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "PATH=test-path") {
		t.Fatalf("supervisor environment is missing PATH: %q", environment)
	}
	for _, forbidden := range []string{"DONETHEN_TEST_SECRET", "must-not-be-inherited", "OPENAI_API_KEY"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("supervisor environment inherited %q: %q", forbidden, environment)
		}
	}
}
