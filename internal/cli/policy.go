package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/hostauthority"
	"github.com/andyandymike/done-then/internal/powerpolicy"
	"github.com/andyandymike/done-then/internal/supervisor"
)

func policyCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printPolicyUsage(streams.Stdout)
		return 0
	}
	if args[0] != "capture" {
		return usageError(streams.Stderr, errors.New("policy supports only the capture subcommand"))
	}
	flags := flag.NewFlagSet("policy capture", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	codexPath := flags.String("codex-path", "", "Codex executable or standard shim")
	pluginID := flags.String("plugin-id", "", "exact App Server plugin id")
	apply := flags.Bool("apply", false, "write the reviewed identity policy without enabling public Plugin execute")
	allowAgentOnly := flags.Bool("allow-agent-only-success", false, "permit explicitly approved execute jobs without a verifier")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError(streams.Stderr, err)
	}
	if flags.NArg() != 0 || *pluginID == "" {
		return usageError(streams.Stderr, errors.New("policy capture requires --plugin-id and no positional arguments"))
	}
	executable, err := deps.resolveCodex(*codexPath)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "resolve Codex", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "resolve current workspace", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	proxy, err := hostauthority.StartProxyWithArgs(probeCtx, executable.Path, executable.PrefixArgs, Version, streams.Stderr)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "start App Server policy probe", err)
	}
	defer proxy.Close()
	var response struct {
		Data []hostauthority.HookInventory `json:"data"`
	}
	if err := proxy.Client().Call(probeCtx, "hooks/list", map[string]any{"cwds": []string{cwd}}, &response); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "read effective Hook inventory", err)
	}
	if len(response.Data) != 1 {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "read effective Hook inventory", errors.New("hooks/list did not return exactly one workspace"))
	}
	if !hostauthority.WorkspacePathMatches(response.Data[0].CWD, cwd) {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "read effective Hook inventory", errors.New("hooks/list returned a different workspace"))
	}
	hashes, err := captureDoneThenHashes(response.Data[0], *pluginID)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "capture DoneThen Hook identity", err)
	}
	decision := hostauthority.EvaluateHooks(response.Data[0], *pluginID, hashes)
	if !decision.Compatible {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "validate effective Hook inventory", errors.New("the current Hook inventory has a conflict; no policy was written"))
	}
	policy := powerpolicy.Policy{
		SchemaVersion: 1, ExecuteEnabled: true, CodexExecutable: executable.Path,
		CodexPrefixArgs: executable.PrefixArgs, ExpectedPluginID: *pluginID,
		ExpectedHookHashes: hashes, AllowAgentOnlySuccess: *allowAgentOnly,
	}
	keys := make([]string, 0, len(hashes))
	for key := range hashes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintf(streams.Stdout, "[DoneThen] Policy capture plan for %s\n", cwd)
	fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin id: %s; trusted Hook definitions: %d; agent-only allowed: %t\n", *pluginID, len(keys), *allowAgentOnly)
	for _, key := range keys {
		fmt.Fprintf(streams.Stdout, "  %s  %s\n", key, shortFingerprint(hashes[key]))
	}
	if !*apply {
		fmt.Fprintln(streams.Stdout, "[DoneThen] Plan only. Re-run with --apply to write the owner-controlled policy; no capability changed.")
		return 0
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	installed, err := powerpolicy.Install(root, policy)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "install power policy", err)
	}
	fmt.Fprintf(streams.Stdout, "[DoneThen] Policy installed at %s (%s). This records reviewed Hook identities but does not enable Plugin execute while authoritative same-host attachment is unavailable.\n", powerpolicy.Path(root), shortFingerprint(installed.Fingerprint))
	return 0
}

func captureDoneThenHashes(inventory hostauthority.HookInventory, pluginID string) (map[string]string, error) {
	hashes := make(map[string]string)
	required := map[string]bool{"posttooluse": false, "userpromptsubmit": false, "stop": false, "sessionend": false}
	for _, hook := range inventory.Hooks {
		if !hook.Enabled || hook.PluginID == nil || *hook.PluginID != pluginID {
			continue
		}
		if hook.Key == "" || hook.CurrentHash == "" {
			return nil, errors.New("an enabled DoneThen Hook is missing its stable key or definition hash")
		}
		hashes[hook.Key] = hook.CurrentHash
		eventName := strings.ToLower(strings.TrimSpace(hook.EventName))
		if _, ok := required[eventName]; ok {
			required[eventName] = true
		}
	}
	missing := make([]string, 0)
	for eventName, found := range required {
		if !found {
			missing = append(missing, eventName)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("required DoneThen Hook events are missing: %v", missing)
	}
	return hashes, nil
}
