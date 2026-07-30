package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/andyandymike/done-then/internal/completion"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "exec" {
		fmt.Fprintln(os.Stderr, "fake-codex expects the exec subcommand")
		os.Exit(2)
	}
	if code := os.Getenv("DONETHEN_FAKE_CODEX_EXIT"); code != "" {
		value, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid DONETHEN_FAKE_CODEX_EXIT")
			os.Exit(2)
		}
		os.Exit(value)
	}
	responsePath := optionValue(os.Args[2:], "--output-last-message")
	if responsePath == "" {
		fmt.Fprintln(os.Stderr, "fake-codex did not receive --output-last-message")
		os.Exit(2)
	}
	status := completion.Status(os.Getenv("DONETHEN_FAKE_CODEX_STATUS"))
	if status == "" {
		status = completion.StatusDone
	}
	remaining := []string{}
	if status != completion.StatusDone {
		remaining = []string{"fixture intentionally reports incomplete work"}
	}
	envelope := completion.Envelope{
		SchemaVersion: "1",
		Status:        status,
		Summary:       "fake Codex fixture result",
		Checks: []completion.Check{{
			Name:     "fixture",
			Status:   completion.CheckPassed,
			Evidence: "deterministic fixture",
		}},
		RemainingWork:    remaining,
		ApprovalRequired: strings.EqualFold(os.Getenv("DONETHEN_FAKE_CODEX_APPROVAL"), "true"),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(responsePath, data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "fake Codex fixture completed")
}

func optionValue(args []string, name string) string {
	for index, arg := range args {
		if arg == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
