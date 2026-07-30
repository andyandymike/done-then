package verifier

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerExitCodes(t *testing.T) {
	t.Setenv("DONETHEN_VERIFIER_HELPER", "1")
	tests := []struct {
		name     string
		exitCode string
		wantCode int
		wantErr  bool
	}{
		{name: "success", exitCode: "0", wantCode: 0},
		{name: "failure", exitCode: "3", wantCode: 3, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			runner := Runner{
				Program: os.Args[0],
				Args:    []string{"-test.run=TestVerifierHelperProcess", "--", test.exitCode},
				Dir:     t.TempDir(),
				Timeout: 5 * time.Second,
				Stdout:  &output,
				Stderr:  &output,
			}
			result, err := runner.Run(context.Background())
			if result.ExitCode != test.wantCode || (err != nil) != test.wantErr {
				t.Fatalf("Run() = %#v, %v", result, err)
			}
			if !strings.Contains(output.String(), "verifier helper") {
				t.Fatalf("output was not forwarded: %q", output.String())
			}
		})
	}
}

func TestRunnerTimeout(t *testing.T) {
	t.Setenv("DONETHEN_VERIFIER_HELPER", "1")
	runner := Runner{
		Program: os.Args[0],
		Args:    []string{"-test.run=TestVerifierHelperProcess", "--", "sleep"},
		Dir:     t.TempDir(),
		Timeout: 50 * time.Millisecond,
	}
	result, err := runner.Run(context.Background())
	if err == nil || result.ExitCode != -1 || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestResolveProgramUsesVerifierWorkingDirectoryForRelativePaths(t *testing.T) {
	workingDir := t.TempDir()
	want := filepath.Join(workingDir, "tools", "verify.exe")
	if got := resolveProgram(filepath.Join("tools", "verify.exe"), workingDir); got != want {
		t.Fatalf("resolveProgram() = %q, want %q", got, want)
	}
	if got := resolveProgram("go", workingDir); got != "go" {
		t.Fatalf("resolveProgram(PATH name) = %q", got)
	}
}

func TestVerifierHelperProcess(t *testing.T) {
	if os.Getenv("DONETHEN_VERIFIER_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "sleep" {
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	os.Stdout.WriteString("verifier helper stdout\n")
	if mode == "3" {
		os.Exit(3)
	}
	os.Exit(0)
}
