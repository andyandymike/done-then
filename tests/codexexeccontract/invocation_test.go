package codexexeccontract_test

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/andyandymike/done-then/internal/codexexec"
)

func TestParseInvocation(t *testing.T) {
	base := t.TempDir()
	invocation, err := ParseInvocation([]string{
		"codex", "exec", "-C", "project", "--model", "test-model", "do the work",
	}, base, false)
	if err != nil {
		t.Fatalf("ParseInvocation() error = %v", err)
	}
	wantDir := filepath.Join(base, "project")
	if invocation.WorkingDir != wantDir {
		t.Fatalf("WorkingDir = %q, want %q", invocation.WorkingDir, wantDir)
	}
	if invocation.Prompt != "do the work" || invocation.PromptFromStdin {
		t.Fatalf("Invocation prompt = %#v", invocation)
	}
}

func TestParseInvocationStdin(t *testing.T) {
	invocation, err := ParseInvocation([]string{"codex.exe", "exec", "-"}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !invocation.PromptFromStdin {
		t.Fatal("PromptFromStdin = false")
	}
}

func TestParseInvocationAllowsPromptBeginningWithDashViaOptionSeparator(t *testing.T) {
	invocation, err := ParseInvocation([]string{"codex", "exec", "--", "-prompt"}, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Prompt != "-prompt" || len(invocation.Options) != 1 || invocation.Options[0] != "--" {
		t.Fatalf("ParseInvocation() = %#v", invocation)
	}
}

func TestParseInvocationRejectsManagedAndDangerousFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "managed schema", args: []string{"codex", "exec", "--output-schema", "x", "prompt"}, want: "managed"},
		{name: "managed output", args: []string{"codex", "exec", "-o", "x", "prompt"}, want: "managed"},
		{name: "yolo", args: []string{"codex", "exec", "--yolo", "prompt"}, want: "requires"},
		{name: "danger sandbox", args: []string{"codex", "exec", "--sandbox", "danger-full-access", "prompt"}, want: "requires"},
		{name: "danger compact sandbox", args: []string{"codex", "exec", "-sdanger-full-access", "prompt"}, want: "requires"},
		{name: "danger config override", args: []string{"codex", "exec", "-c", `sandbox_mode="danger-full-access"`, "prompt"}, want: "requires"},
		{name: "danger compact config", args: []string{"codex", "exec", `-csandbox_mode="danger-full-access"`, "prompt"}, want: "requires"},
		{name: "resume", args: []string{"codex", "exec", "resume", "id"}, want: "not supported"},
		{name: "review subcommand", args: []string{"codex", "exec", "review", "prompt"}, want: "not supported"},
		{name: "ignore rules", args: []string{"codex", "exec", "--ignore-rules", "prompt"}, want: "requires"},
		{name: "wrong command", args: []string{"other", "exec", "prompt"}, want: "codex exec"},
		{name: "extra positional after separator", args: []string{"codex", "exec", "--", "extra", "prompt"}, want: "immediately before"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseInvocation(test.args, t.TempDir(), false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseInvocation() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseInvocationAllowsDangerousFlagsOnlyExplicitly(t *testing.T) {
	invocation, err := ParseInvocation(
		[]string{"codex", "exec", "--sandbox=danger-full-access", "prompt"},
		t.TempDir(),
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(invocation.DangerousFlags) != 1 {
		t.Fatalf("DangerousFlags = %#v", invocation.DangerousFlags)
	}
}

func TestComposePromptAddsContractWithoutChangingPrefix(t *testing.T) {
	user := "implement exactly this"
	combined := ComposePrompt(user)
	if !strings.HasPrefix(combined, user+"\n\n") {
		t.Fatalf("ComposePrompt() changed user prompt prefix: %q", combined)
	}
	if !strings.Contains(combined, "final response must be the JSON object") {
		t.Fatalf("ComposePrompt() missing completion contract: %q", combined)
	}
}
