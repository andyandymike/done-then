package verifier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/processgroup"
)

type Runner struct {
	Program string
	Args    []string
	Dir     string
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
}

type Result struct {
	ExitCode int
	Duration time.Duration
}

func (r Runner) Run(ctx context.Context) (Result, error) {
	started := time.Now()
	if r.Program == "" {
		return Result{ExitCode: -1}, errors.New("verifier program is empty")
	}
	if r.Timeout <= 0 {
		return Result{ExitCode: -1}, errors.New("verifier timeout must be positive")
	}

	verifyCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	program := resolveProgram(r.Program, r.Dir)
	command := exec.CommandContext(verifyCtx, program, r.Args...)
	command.Dir = r.Dir
	command.Stdout = r.Stdout
	command.Stderr = r.Stderr
	if err := command.Start(); err != nil {
		return Result{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("start verifier: %w", err)
	}
	group, err := processgroup.Attach(command.Process)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return Result{ExitCode: -1, Duration: time.Since(started)}, fmt.Errorf("isolate verifier process tree: %w", err)
	}
	err = command.Wait()
	groupErr := group.Close()
	result := Result{ExitCode: 0, Duration: time.Since(started)}
	if groupErr != nil {
		result.ExitCode = -1
		return result, fmt.Errorf("close verifier process tree: %w", groupErr)
	}
	if err == nil {
		return result, nil
	}
	if errors.Is(verifyCtx.Err(), context.DeadlineExceeded) {
		result.ExitCode = -1
		return result, fmt.Errorf("verifier timed out after %s", r.Timeout)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, fmt.Errorf("verifier exited with code %d", result.ExitCode)
	}
	result.ExitCode = -1
	return result, fmt.Errorf("start verifier: %w", err)
}

func resolveProgram(program, workingDir string) string {
	if filepath.IsAbs(program) {
		return program
	}
	if strings.ContainsAny(program, `/\`) {
		return filepath.Join(workingDir, program)
	}
	return program
}
