package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/capability"
	"github.com/andyandymike/done-then/internal/hostauthority"
	"github.com/andyandymike/done-then/internal/powerpolicy"
	"github.com/andyandymike/done-then/internal/supervisor"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	Version                         string            `json:"version"`
	Platform                        string            `json:"platform"`
	CapabilityLevel                 string            `json:"capability_level"`
	CapabilityStatement             string            `json:"capability_statement"`
	ExecuteAvailable                bool              `json:"execute_available"`
	AfterStopExecuteAvailable       bool              `json:"after_stop_execute_available"`
	AfterAllStopExecuteAvailable    bool              `json:"after_all_stop_execute_available"`
	VerifiedSuccessExecuteAvailable bool              `json:"verified_success_execute_available"`
	StopArbitrationAvailable        bool              `json:"stop_arbitration_available"`
	BuildSupportedByPolicy          map[string]bool   `json:"build_supported_by_policy"`
	BackendSupportedByPolicy        map[string]bool   `json:"backend_supported_by_policy"`
	BackendPreflightByPolicy        map[string]bool   `json:"backend_preflight_passed_by_policy"`
	ExecuteReadyByPolicy            map[string]bool   `json:"execute_ready_by_policy"`
	UnavailableReasonsByPolicy      map[string]string `json:"execute_unavailable_reasons_by_policy"`
	Checks                          []doctorCheck     `json:"checks"`
}

func doctorCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	jsonOutput := false
	if len(args) == 1 && args[0] == "--json" {
		jsonOutput = true
	} else if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printDoctorUsage(streams.Stdout)
		return 0
	} else if len(args) != 0 {
		return usageError(streams.Stderr, errors.New("doctor accepts only --json"))
	}
	report := doctorReport{
		Version: Version, Platform: runtime.GOOS + "-" + runtime.GOARCH,
		BuildSupportedByPolicy: map[string]bool{
			"after_stop": true, "after_all_stop": true, "verified_success": true,
		},
		BackendSupportedByPolicy: map[string]bool{}, BackendPreflightByPolicy: map[string]bool{},
		ExecuteReadyByPolicy: map[string]bool{}, UnavailableReasonsByPolicy: map[string]string{},
	}
	platformCapability, found, manifestErr := capability.Current(runtime.GOOS, runtime.GOARCH)
	if manifestErr != nil {
		report.Checks = append(report.Checks, doctorCheck{"capability_manifest", "FAIL", manifestErr.Error()})
	} else if !found {
		report.Checks = append(report.Checks, doctorCheck{"capability_manifest", "WARN", "this OS/architecture has no published capability entry"})
	} else {
		report.CapabilityLevel = platformCapability.Level
		report.CapabilityStatement = platformCapability.Statement
		report.Checks = append(report.Checks, doctorCheck{"capability_manifest", "PASS", platformCapability.Level + ": " + platformCapability.Statement})
	}
	root, err := deps.dataRoot()
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{"state_directory", "FAIL", err.Error()})
		return writeDoctorReport(report, jsonOutput, streams)
	}
	absoluteRoot, _ := filepath.Abs(root)
	if info, statErr := os.Lstat(absoluteRoot); errors.Is(statErr, os.ErrNotExist) {
		report.Checks = append(report.Checks, doctorCheck{"state_directory", "WARN", "state directory does not exist yet: " + absoluteRoot})
	} else if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		report.Checks = append(report.Checks, doctorCheck{"state_directory", "FAIL", "state directory is unavailable or unsafe"})
	} else {
		report.Checks = append(report.Checks, doctorCheck{"state_directory", "PASS", absoluteRoot})
	}
	profiles, profileErr := verifierprofile.New(root)
	if profileErr != nil {
		report.Checks = append(report.Checks, doctorCheck{"verifier_profiles", "FAIL", profileErr.Error()})
	} else if ids, listErr := profiles.List(); listErr != nil {
		report.Checks = append(report.Checks, doctorCheck{"verifier_profiles", "FAIL", listErr.Error()})
	} else if len(ids) == 0 {
		report.Checks = append(report.Checks, doctorCheck{"verifier_profiles", "WARN", "no registered verifier profiles"})
	} else {
		report.Checks = append(report.Checks, doctorCheck{"verifier_profiles", "PASS", fmt.Sprintf("%d registered profile(s)", len(ids))})
	}

	request := actions.PowerRequest{JobID: "dt_doctor_probe", Action: "shutdown", Delay: 2 * time.Minute, Comment: "DoneThen job dt_doctor_probe completed", RequestedAt: time.Now().UTC()}
	capabilities, backendErr := deps.newActionBackend().Preflight(ctx, request)
	backendSupported := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	backendPreflightPassed := backendErr == nil && capabilities.ExecuteSupported
	for _, policy := range []string{"after_stop", "after_all_stop", "verified_success"} {
		report.BackendSupportedByPolicy[policy] = backendSupported
		report.BackendPreflightByPolicy[policy] = backendPreflightPassed
	}
	if backendErr != nil || !capabilities.ExecuteSupported {
		detail := capabilities.Reason
		if detail == "" && backendErr != nil {
			detail = backendErr.Error()
		}
		report.Checks = append(report.Checks, doctorCheck{"power_backend", "WARN", boundedDoctorDetail(detail)})
	} else {
		report.Checks = append(report.Checks, doctorCheck{"power_backend", "PASS", fmt.Sprintf("%s; cancel scope=%s", capabilities.BackendID, capabilities.CancelScope)})
	}
	report.Checks = append(report.Checks, doctorCheck{
		"stop_arbitration", "WARN",
		"Codex exposes concurrent Stop hooks but no trusted final arbitration receipt; after_stop and after_all_stop execute remain disabled",
	})

	policy, policyErr := powerpolicy.Load(root)
	if policyErr != nil {
		report.Checks = append(report.Checks, doctorCheck{"power_policy", "INFO", "verified-success execute is unavailable; after-stop does not require this policy"})
	} else {
		report.Checks = append(report.Checks, doctorCheck{"power_policy", "PASS", "owner-controlled policy fingerprint " + shortFingerprint(policy.Fingerprint)})
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			report.Checks = append(report.Checks, doctorCheck{"app_server", "FAIL", "current workspace is unavailable"})
		} else if check := probeAppServer(ctx, policy, cwd); check.Status != "PASS" {
			report.Checks = append(report.Checks, check)
		} else {
			report.Checks = append(report.Checks, check)
			report.VerifiedSuccessExecuteAvailable = backendErr == nil && capabilities.ExecuteSupported && profiles != nil
		}
	}
	report.Checks = append(report.Checks,
		doctorCheck{"machine_lock", "INFO", "not acquired by doctor; the final action path acquires it without scheduling"},
		doctorCheck{"release_signature", "INFO", "signature and attestation acceptance is platform-release evidence, not inferred from source"},
	)
	report.ExecuteAvailable = report.AfterStopExecuteAvailable || report.AfterAllStopExecuteAvailable || report.VerifiedSuccessExecuteAvailable
	report.ExecuteReadyByPolicy["after_stop"] = report.AfterStopExecuteAvailable
	report.ExecuteReadyByPolicy["after_all_stop"] = report.AfterAllStopExecuteAvailable
	report.ExecuteReadyByPolicy["verified_success"] = report.VerifiedSuccessExecuteAvailable
	report.UnavailableReasonsByPolicy["after_stop"] = "stop_arbitration_unavailable"
	report.UnavailableReasonsByPolicy["after_all_stop"] = "stop_arbitration_unavailable"
	if !report.VerifiedSuccessExecuteAvailable {
		report.UnavailableReasonsByPolicy["verified_success"] = "verified_success_authority_unavailable"
	}
	return writeDoctorReport(report, jsonOutput, streams)
}

func probeAppServer(ctx context.Context, policy powerpolicy.Policy, cwd string) doctorCheck {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	proxy, err := hostauthority.StartProxyWithArgs(probeCtx, policy.CodexExecutable, policy.CodexPrefixArgs, Version, nil)
	if err != nil {
		return doctorCheck{"app_server", "FAIL", boundedDoctorDetail(err.Error())}
	}
	defer proxy.Close()
	var response struct {
		Data []hostauthority.HookInventory `json:"data"`
	}
	if err := proxy.Client().Call(probeCtx, "hooks/list", map[string]any{"cwds": []string{cwd}}, &response); err != nil || len(response.Data) != 1 {
		return doctorCheck{"app_server", "FAIL", "hooks/list capability is unavailable or incomplete"}
	}
	decision := hostauthority.EvaluateHooks(response.Data[0], policy.ExpectedPluginID, policy.ExpectedHookHashes)
	if !decision.Compatible {
		return doctorCheck{"app_server", "FAIL", "effective hook inventory conflicts with the local power policy"}
	}
	return doctorCheck{"app_server", "WARN", "hooks/list is available through an isolated proxy, but authoritative same-host attachment is unavailable"}
}

func writeDoctorReport(report doctorReport, jsonOutput bool, streams IO) int {
	if jsonOutput {
		encoder := json.NewEncoder(streams.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "write doctor report", err)
		}
		return 0
	}
	fmt.Fprintf(streams.Stdout, "DoneThen %s on %s — capability %s\n", report.Version, report.Platform, valueOr(report.CapabilityLevel, "unlisted"))
	if report.CapabilityStatement != "" {
		fmt.Fprintln(streams.Stdout, report.CapabilityStatement)
	}
	writer := tabwriter.NewWriter(streams.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "CHECK\tSTATUS\tDETAIL")
	for _, check := range report.Checks {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Name, check.Status, check.Detail)
	}
	_ = writer.Flush()
	fmt.Fprintf(streams.Stdout, "After-stop execute available now: %t\n", report.AfterStopExecuteAvailable)
	fmt.Fprintf(streams.Stdout, "After-all-stop execute available now: %t\n", report.AfterAllStopExecuteAvailable)
	fmt.Fprintf(streams.Stdout, "Verified-success execute available now: %t\n", report.VerifiedSuccessExecuteAvailable)
	return 0
}

func boundedDoctorDetail(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	if value == "" {
		return "capability is unavailable"
	}
	return value
}

func shortFingerprint(value string) string {
	if len(value) > 20 {
		return value[:20]
	}
	return value
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
