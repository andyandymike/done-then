package codexexec

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const MaxPromptBytes = 1 << 20

const completionContract = `DoneThen completion reporting contract:
- Return status=done only if the requested outcome is actually achieved.
- Required verification must have completed successfully.
- Use partial when useful work is complete but requested work remains.
- Use blocked when external input, approval, or state is required.
- Use failed when the requested outcome was not achieved because of an error.
- Report every known remaining item.
- Do not mark checks as passed without concrete evidence.
- Do not invoke shutdown, sleep, hibernate, lock, or any other post-task action; DoneThen owns that action.
- Your final response must be the JSON object required by the supplied output schema.`

type Invocation struct {
	Options         []string
	Prompt          string
	PromptFromStdin bool
	WorkingDir      string
	DangerousFlags  []string
}

func ParseInvocation(args []string, currentDir string, allowDangerous bool) (Invocation, error) {
	if len(args) < 3 {
		return Invocation{}, errors.New("expected: codex exec [options] PROMPT")
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(args[0])), strings.ToLower(filepath.Ext(args[0])))
	if name != "codex" || args[1] != "exec" {
		return Invocation{}, errors.New("command after -- must begin with codex exec")
	}
	if len(args) > 2 {
		switch args[2] {
		case "resume", "review", "help":
			return Invocation{}, fmt.Errorf("codex exec %s is not supported by the v0.1 adapter", args[2])
		}
	}
	prompt := args[len(args)-1]
	if prompt == "" {
		return Invocation{}, errors.New("Codex prompt must not be empty")
	}
	options := append([]string(nil), args[2:len(args)-1]...)
	separatorPresent := false
	for index, option := range options {
		if option == "--" {
			if separatorPresent || index != len(options)-1 {
				return Invocation{}, errors.New("Codex option separator -- must appear exactly once and immediately before the final prompt")
			}
			separatorPresent = true
		}
	}
	if strings.HasPrefix(prompt, "-") && prompt != "-" && !separatorPresent {
		return Invocation{}, errors.New("a Codex prompt beginning with - requires a Codex option separator --")
	}
	if err := rejectManagedFlags(options); err != nil {
		return Invocation{}, err
	}
	dangerous := findDangerousFlags(options)
	if len(dangerous) != 0 && !allowDangerous {
		return Invocation{}, fmt.Errorf("dangerous Codex flag %q requires --allow-dangerous-codex-flags", dangerous[0])
	}
	cwd, err := extractWorkingDir(options, currentDir)
	if err != nil {
		return Invocation{}, err
	}
	return Invocation{
		Options:         options,
		Prompt:          prompt,
		PromptFromStdin: prompt == "-",
		WorkingDir:      cwd,
		DangerousFlags:  dangerous,
	}, nil
}

func ComposePrompt(prompt string) string {
	return prompt + "\n\n" + completionContract + "\n"
}

func rejectManagedFlags(options []string) error {
	for _, option := range options {
		lower := strings.ToLower(option)
		if lower == "--output-schema" || strings.HasPrefix(lower, "--output-schema=") ||
			lower == "--output-last-message" || strings.HasPrefix(lower, "--output-last-message=") ||
			lower == "-o" || strings.HasPrefix(lower, "-o=") ||
			(strings.HasPrefix(lower, "-o") && !strings.HasPrefix(lower, "--")) {
			return fmt.Errorf("Codex flag %q is managed by DoneThen", option)
		}
	}
	return nil
}

func findDangerousFlags(options []string) []string {
	found := make([]string, 0)
	for index := 0; index < len(options); index++ {
		option := strings.ToLower(options[index])
		switch {
		case option == "--dangerously-bypass-approvals-and-sandbox",
			option == "--yolo",
			option == "--dangerously-bypass-hook-trust",
			option == "--ignore-rules":
			found = append(found, options[index])
		case option == "--sandbox" || option == "-s":
			if index+1 < len(options) && strings.EqualFold(options[index+1], "danger-full-access") {
				found = append(found, options[index]+" "+options[index+1])
			}
		case strings.EqualFold(option, "--sandbox=danger-full-access"),
			strings.EqualFold(option, "-s=danger-full-access"):
			found = append(found, options[index])
		case strings.HasPrefix(option, "-s") && !strings.HasPrefix(option, "--"):
			value := strings.TrimPrefix(strings.TrimPrefix(option, "-s"), "=")
			if strings.EqualFold(value, "danger-full-access") {
				found = append(found, options[index])
			}
		case option == "-c" || option == "--config":
			if index+1 < len(options) && isDangerousConfigOverride(options[index+1]) {
				found = append(found, options[index]+" "+options[index+1])
			}
		case strings.HasPrefix(option, "-c=") || strings.HasPrefix(option, "--config="):
			if isDangerousConfigOverride(option) {
				found = append(found, options[index])
			}
		case strings.HasPrefix(option, "-c") && !strings.HasPrefix(option, "--"):
			value := strings.TrimPrefix(strings.TrimPrefix(option, "-c"), "=")
			if isDangerousConfigOverride(value) {
				found = append(found, options[index])
			}
		}
	}
	return found
}

func isDangerousConfigOverride(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(value, " ", ""))
	return strings.Contains(normalized, "sandbox_mode") && strings.Contains(normalized, "danger-full-access")
}

func extractWorkingDir(options []string, currentDir string) (string, error) {
	value := ""
	for index := 0; index < len(options); index++ {
		option := options[index]
		switch {
		case option == "-C" || option == "--cd":
			if index+1 >= len(options) {
				return "", fmt.Errorf("Codex flag %s requires a directory", option)
			}
			value = options[index+1]
			if value == "--" || value == "" {
				return "", fmt.Errorf("Codex flag %s requires a non-empty directory", option)
			}
			index++
		case strings.HasPrefix(option, "--cd="):
			value = strings.TrimPrefix(option, "--cd=")
		case strings.HasPrefix(option, "-C="):
			value = strings.TrimPrefix(option, "-C=")
		}
	}
	if value == "" {
		return filepath.Abs(currentDir)
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(currentDir, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve Codex working directory: %w", err)
	}
	return absolute, nil
}
