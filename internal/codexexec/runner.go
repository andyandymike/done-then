package codexexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/processgroup"
	"github.com/andyandymike/done-then/internal/supervisor"
)

type Runner struct {
	Executable     Executable
	Invocation     Invocation
	CombinedPrompt string
	ArtifactDir    string
	TaskTimeout    time.Duration
	KeepArtifacts  bool
	Stdout         io.Writer
	Stderr         io.Writer
}

func (r Runner) Run(ctx context.Context) (supervisor.AgentResult, error) {
	started := time.Now()
	result := supervisor.AgentResult{ExitCode: -1}
	if r.Executable.Path == "" {
		return result, errors.New("Codex executable path is empty")
	}
	if r.TaskTimeout <= 0 {
		return result, errors.New("Codex task timeout must be positive")
	}
	if r.ArtifactDir == "" {
		return result, errors.New("Codex artifact directory is empty")
	}
	if !r.KeepArtifacts {
		defer os.RemoveAll(r.ArtifactDir)
	}
	if err := os.MkdirAll(r.ArtifactDir, 0o700); err != nil {
		return result, fmt.Errorf("create Codex artifact directory: %w", err)
	}
	schemaPath := filepath.Join(r.ArtifactDir, "completion-envelope.schema.json")
	responsePath := filepath.Join(r.ArtifactDir, "final-response.json")
	if err := os.WriteFile(schemaPath, completion.SchemaJSON, 0o600); err != nil {
		return result, fmt.Errorf("write completion schema: %w", err)
	}
	_ = os.Remove(responsePath)

	args := buildCommandArgs(r.Executable, r.Invocation, schemaPath, responsePath, r.CombinedPrompt)

	taskCtx, cancel := context.WithTimeout(ctx, r.TaskTimeout)
	defer cancel()
	command := exec.CommandContext(taskCtx, r.Executable.Path, args...)
	command.Stdout = r.Stdout
	command.Stderr = r.Stderr
	if r.Invocation.PromptFromStdin {
		command.Stdin = stringsReader(r.CombinedPrompt)
	}
	if err := command.Start(); err != nil {
		result.Duration = time.Since(started)
		return result, fmt.Errorf("start Codex: %w", err)
	}
	result.PID = command.Process.Pid
	group, err := processgroup.Attach(command.Process)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		result.Duration = time.Since(started)
		return result, fmt.Errorf("isolate Codex process tree: %w", err)
	}
	waitErr := command.Wait()
	groupErr := group.Close()
	result.Duration = time.Since(started)
	if groupErr != nil {
		return result, fmt.Errorf("close Codex process tree: %w", groupErr)
	}
	if waitErr != nil {
		if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
			return result, fmt.Errorf("Codex timed out after %s", r.TaskTimeout)
		}
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			result.ExitCode = exitError.ExitCode()
			return result, fmt.Errorf("Codex exited with code %d", result.ExitCode)
		}
		return result, fmt.Errorf("wait for Codex: %w", waitErr)
	}
	result.ExitCode = 0
	result.Completion, result.CompletionErr = readCompletion(responsePath)
	return result, nil
}

func buildCommandArgs(executable Executable, invocation Invocation, schemaPath, responsePath, combinedPrompt string) []string {
	args := append([]string(nil), executable.PrefixArgs...)
	args = append(args, "exec")
	separator := len(invocation.Options)
	for index, option := range invocation.Options {
		if option == "--" {
			separator = index
			break
		}
	}
	args = append(args, invocation.Options[:separator]...)
	args = append(args,
		"--output-schema", schemaPath,
		"--output-last-message", responsePath,
	)
	args = append(args, invocation.Options[separator:]...)
	prompt := combinedPrompt
	if invocation.PromptFromStdin {
		prompt = "-"
	}
	return append(args, prompt)
}

func readCompletion(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Codex final response: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, completion.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex final response: %w", err)
	}
	if len(data) > completion.MaxResponseBytes {
		return nil, fmt.Errorf("Codex final response exceeds %d bytes", completion.MaxResponseBytes)
	}
	return data, nil
}

type stringReader struct {
	value  string
	offset int
}

func stringsReader(value string) io.Reader {
	return &stringReader{value: value}
}

func (r *stringReader) Read(buffer []byte) (int, error) {
	if r.offset >= len(r.value) {
		return 0, io.EOF
	}
	count := copy(buffer, r.value[r.offset:])
	r.offset += count
	return count, nil
}
