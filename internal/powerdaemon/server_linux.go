//go:build linux

package powerdaemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/filetrust"
	basestore "github.com/andyandymike/done-then/internal/store"
)

const (
	daemonProtocolVersion = 1
	maxDaemonMessageBytes = 64 << 10
)

type daemonRequest struct {
	SchemaVersion int                   `json:"schema_version"`
	Operation     string                `json:"operation"`
	Request       *actions.PowerRequest `json:"request,omitempty"`
	Receipt       *actions.Receipt      `json:"receipt,omitempty"`
}

type daemonResponse struct {
	SchemaVersion int                      `json:"schema_version"`
	OK            bool                     `json:"ok"`
	ErrorCode     string                   `json:"error_code,omitempty"`
	Message       string                   `json:"message,omitempty"`
	Capabilities  *actions.Capabilities    `json:"capabilities,omitempty"`
	Receipt       *actions.Receipt         `json:"receipt,omitempty"`
	CancelResult  *actions.CancelResult    `json:"cancel_result,omitempty"`
	Reconcile     *actions.ReconcileResult `json:"reconcile_result,omitempty"`
}

type activeState struct {
	SchemaVersion int             `json:"schema_version"`
	OwnerUID      uint32          `json:"owner_uid"`
	Phase         string          `json:"phase"`
	Receipt       actions.Receipt `json:"receipt"`
}

type server struct {
	config Config
	mu     sync.Mutex
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, executable string, args ...string) (int, []byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.CombinedOutput()
	if len(output) > 4096 {
		output = output[:4096]
	}
	if err == nil {
		return 0, output, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), output, err
	}
	return -1, output, err
}

func Run(ctx context.Context, config Config) error {
	if os.Geteuid() != 0 {
		return errors.New("the Linux power helper must run as root")
	}
	config = withDefaults(config)
	if err := prepareRootDirectory(config.StateDirectory); err != nil {
		return err
	}
	lock, err := acquireDaemonLock(filepath.Join(config.StateDirectory, "daemon.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := prepareSocketPath(config.SocketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.SocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on power helper socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(config.SocketPath)
	if err := secureSocket(config.SocketPath, config.GroupName); err != nil {
		return err
	}
	s := &server{config: config}
	for {
		if err := listener.SetDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("accept power helper connection: %w", err)
		}
		go s.serveConnection(ctx, connection)
	}
}

func (s *server) serveConnection(ctx context.Context, connection *net.UnixConn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	uid, err := peerUID(connection)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(failure("peer_rejected", "peer credentials are unavailable"))
		return
	}
	request, err := decodeDaemonRequest(connection)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(failure("invalid_request", err.Error()))
		return
	}
	response := s.process(ctx, uid, request)
	_ = json.NewEncoder(connection).Encode(response)
}

func (s *server) process(ctx context.Context, uid uint32, request daemonRequest) daemonResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	stateLock, err := acquireActiveStateLock(filepath.Join(s.config.StateDirectory, "active.lock"), 2*time.Second)
	if err != nil {
		return failure("state_busy", "helper state is busy; retry without scheduling another action")
	}
	defer stateLock.Close()
	if request.SchemaVersion != daemonProtocolVersion {
		return failure("invalid_request", "unsupported helper protocol")
	}
	switch request.Operation {
	case "preflight":
		return s.preflight(request.Request)
	case "schedule":
		return s.schedule(ctx, uid, request.Request)
	case "cancel":
		return s.cancel(ctx, uid, request.Receipt)
	case "reconcile", "status":
		return s.reconcile(ctx, uid, request.Receipt)
	default:
		return failure("invalid_request", "unsupported helper operation")
	}
}

func (s *server) preflight(request *actions.PowerRequest) daemonResponse {
	if request == nil {
		return failure("invalid_request", "preflight requires a power request")
	}
	capabilities := helperCapabilities()
	if err := validateHelperRequest(*request); err != nil {
		return failure("policy_rejected", err.Error())
	}
	if err := s.config.HostCheck(); err != nil {
		return failure("platform_unsupported", err.Error())
	}
	if _, found, err := s.loadActive(); err != nil {
		return failure("state_error", "helper state is unreadable")
	} else if found {
		return failure("active_job_conflict", "another machine power job is unresolved")
	}
	capabilities.ExecuteSupported = true
	capabilities.Reason = ""
	return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, Capabilities: &capabilities}
}

func (s *server) schedule(ctx context.Context, uid uint32, request *actions.PowerRequest) daemonResponse {
	if request == nil {
		return failure("invalid_request", "schedule requires a power request")
	}
	if response := s.preflight(request); !response.OK {
		return response
	}
	now := s.config.Now().UTC()
	token := actions.SystemdUnitToken(request.JobID)
	receipt := actions.SealReceipt(actions.Receipt{
		Platform:       "linux-systemd",
		BackendID:      "linux-systemd-helper",
		BackendVersion: "1",
		JobID:          request.JobID,
		Action:         request.Action,
		RequestedAt:    request.RequestedAt.UTC(),
		ScheduledAt:    now,
		Deadline:       now.Add(request.Delay),
		ExternalToken:  token,
		CancelScope:    actions.CancelScopeJob,
		BootID:         linuxBootID(),
		ResultSummary:  "helper intent persisted before systemd scheduling",
	})
	intent := activeState{SchemaVersion: 1, OwnerUID: uid, Phase: "intent", Receipt: receipt}
	if err := s.saveActive(intent); err != nil {
		return failure("state_error", "could not persist helper action intent")
	}
	seconds := strconv.FormatInt(int64(request.Delay/time.Second), 10) + "s"
	exitCode, _, runErr := s.config.Runner.Run(ctx, s.config.SystemdRunPath,
		"--quiet", "--collect", "--unit="+token, "--on-active="+seconds,
		"--timer-property=AccuracySec=1s", "--property=Type=oneshot",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes",
		"--property=ProtectHome=yes", "--property=RestrictAddressFamilies=AF_UNIX",
		s.config.HelperPath, "--fire-job="+request.JobID, "--state-dir="+s.config.StateDirectory)
	if runErr != nil || exitCode != 0 {
		intent.Phase = "schedule_unverified"
		if err := s.saveActive(intent); err != nil {
			return failure("state_error", "systemd outcome and helper recovery state are both uncertain")
		}
		return failure("schedule_failed", "systemd scheduling outcome is unknown; cancel or reconcile this job")
	}
	receipt.ResultCode = exitCode
	receipt.ResultSummary = "systemd accepted a job-specific transient shutdown timer"
	receipt = actions.SealReceipt(receipt)
	intent.Phase = "scheduled"
	intent.Receipt = receipt
	if err := s.saveActive(intent); err != nil {
		_, _, _ = s.config.Runner.Run(ctx, s.config.SystemctlPath, "stop", token+".timer", token+".service")
		return failure("state_error", "timer accepted but receipt persistence failed; rollback was attempted")
	}
	return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, Receipt: &receipt}
}

func (s *server) cancel(ctx context.Context, uid uint32, receipt *actions.Receipt) daemonResponse {
	if receipt == nil || validateSystemdReceipt(*receipt) != nil {
		return failure("invalid_request", "cancel requires a valid Linux helper receipt")
	}
	active, found, err := s.loadActive()
	if err != nil {
		return failure("state_error", "helper state is unreadable")
	}
	if !found {
		result := actions.CancelResult{NoActionInProgress: true, Scope: actions.CancelScopeJob, ResultSummary: "helper has no active DoneThen job"}
		response := failure("no_action_in_progress", "no DoneThen timer is active")
		response.CancelResult = &result
		return response
	}
	if active.OwnerUID != uid || active.Receipt.JobID != receipt.JobID || active.Receipt.ExternalToken != receipt.ExternalToken {
		return failure("peer_rejected", "receipt does not belong to this peer")
	}
	token := active.Receipt.ExternalToken
	exitCode, _, runErr := s.config.Runner.Run(ctx, s.config.SystemctlPath, "stop", token+".timer", token+".service")
	if runErr != nil || exitCode != 0 {
		inactive, verifyErr := s.unitsInactive(ctx, token)
		if verifyErr != nil || !inactive {
			return failure("cancel_failed", "systemd did not confirm timer cancellation")
		}
	}
	_, _, _ = s.config.Runner.Run(ctx, s.config.SystemctlPath, "reset-failed", token+".timer", token+".service")
	if err := os.Remove(s.activePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failure("state_error", "timer stopped but helper state cleanup failed")
	}
	result := actions.CancelResult{Cancelled: true, Scope: actions.CancelScopeJob, ResultCode: exitCode, ResultSummary: "job-specific systemd timer stopped"}
	return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, CancelResult: &result}
}

func (s *server) unitsInactive(ctx context.Context, token string) (bool, error) {
	for _, unit := range []string{token + ".timer", token + ".service"} {
		exitCode, output, runErr := s.config.Runner.Run(ctx, s.config.SystemctlPath, "is-active", unit)
		status := strings.ToLower(strings.TrimSpace(string(output)))
		if exitCode == 0 || status == "active" || status == "activating" || status == "reloading" || status == "deactivating" {
			return false, nil
		}
		if status != "inactive" && status != "failed" && status != "unknown" {
			if runErr != nil {
				return false, runErr
			}
			return false, fmt.Errorf("unexpected systemd unit state %q", status)
		}
	}
	return true, nil
}

func (s *server) reconcile(ctx context.Context, uid uint32, receipt *actions.Receipt) daemonResponse {
	if receipt == nil || validateSystemdReceipt(*receipt) != nil {
		return failure("invalid_request", "reconcile requires a valid Linux helper receipt")
	}
	active, found, err := s.loadActive()
	if err != nil {
		return failure("state_error", "helper state is unreadable")
	}
	now := s.config.Now().UTC()
	result := actions.ReconcileResult{CheckedAt: now, CurrentBootID: linuxBootID()}
	if !found {
		result.State = actions.ReconcileUnverified
		result.Evidence = "helper has no matching active state and cannot independently prove poweroff"
		return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, Reconcile: &result}
	}
	if active.OwnerUID != uid || active.Receipt.JobID != receipt.JobID || active.Receipt.ExternalToken != receipt.ExternalToken {
		return failure("peer_rejected", "receipt does not belong to this peer")
	}
	inactive, unitErr := s.unitsInactive(ctx, active.Receipt.ExternalToken)
	if active.Phase == "scheduled" && now.Before(active.Receipt.Deadline) && active.Receipt.BootID == result.CurrentBootID && unitErr == nil && !inactive {
		result.State = actions.ReconcileScheduled
		result.Evidence = "helper state and an active job-specific systemd unit match before the recorded deadline"
		return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, Reconcile: &result}
	}
	result.State = actions.ReconcileUnverified
	if unitErr != nil || !inactive {
		result.Evidence = "helper cannot prove execution and the matching systemd units are not confirmed inactive"
		return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, Reconcile: &result}
	}
	if err := os.Remove(s.activePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failure("state_error", "matching systemd units are inactive but helper state cleanup failed")
	}
	result.Evidence = "matching systemd units are inactive; helper recovery state was released without scheduling another action"
	return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: true, Reconcile: &result}
}

func withDefaults(config Config) Config {
	if config.SocketPath == "" {
		config.SocketPath = "/run/donethen/powerd.sock"
	}
	if config.StateDirectory == "" {
		config.StateDirectory = "/var/lib/donethen"
	}
	if config.GroupName == "" {
		config.GroupName = "donethen"
	}
	if config.SystemdRunPath == "" {
		config.SystemdRunPath = "/usr/bin/systemd-run"
	}
	if config.SystemctlPath == "" {
		config.SystemctlPath = "/usr/bin/systemctl"
	}
	if config.HelperPath == "" {
		config.HelperPath = "/usr/local/libexec/donethen/donethen-powerd"
	}
	if config.MaxFireLateness <= 0 {
		config.MaxFireLateness = 30 * time.Second
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Runner == nil {
		config.Runner = execRunner{}
	}
	if config.HostCheck == nil {
		config.HostCheck = func() error { return validateSystemdHost(config) }
	}
	return config
}

func helperCapabilities() actions.Capabilities {
	return actions.Capabilities{
		Platform:           "linux-systemd",
		BackendID:          "linux-systemd-helper",
		ExecuteSupported:   false,
		CancelScope:        actions.CancelScopeJob,
		MinimumDelay:       30 * time.Second,
		MaximumDelay:       time.Hour,
		ReconcileSupported: true,
		Reason:             "helper preflight has not completed",
	}
}

func validateHelperRequest(request actions.PowerRequest) error {
	if err := actions.ValidateRequest(request, 30*time.Second, time.Hour); err != nil {
		return err
	}
	if !actions.IsFixedPowerComment(request.JobID, request.Comment) {
		return errors.New("power comment is not a fixed DoneThen comment")
	}
	return nil
}

func validateSystemdReceipt(receipt actions.Receipt) error {
	if err := actions.ValidateReceipt(receipt); err != nil {
		return err
	}
	if receipt.Platform != "linux-systemd" || receipt.BackendID != "linux-systemd-helper" ||
		receipt.CancelScope != actions.CancelScopeJob || receipt.ExternalToken != actions.SystemdUnitToken(receipt.JobID) {
		return errors.New("receipt is not owned by the Linux helper")
	}
	return nil
}

func validateSystemdHost(config Config) error {
	comm, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(comm)) != "systemd" {
		return errors.New("PID 1 is not systemd")
	}
	if linuxBootID() == "" {
		return errors.New("Linux boot identity is unavailable")
	}
	for _, path := range []string{config.SystemdRunPath, config.SystemctlPath, config.HelperPath} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("required systemd executable is unavailable or unsafe")
		}
	}
	return nil
}

func (s *server) activePath() string { return filepath.Join(s.config.StateDirectory, "active.json") }

func (s *server) saveActive(state activeState) error {
	return basestore.AtomicWriteJSON(s.activePath(), state)
}

func (s *server) loadActive() (activeState, bool, error) {
	file, info, err := filetrust.OpenOwnerControlled(s.activePath(), "active helper state")
	if errors.Is(err, os.ErrNotExist) {
		return activeState{}, false, nil
	}
	if err != nil {
		return activeState{}, false, err
	}
	defer file.Close()
	if info.Size() > maxDaemonMessageBytes || info.Mode().Perm()&0o077 != 0 {
		return activeState{}, false, errors.New("active helper state has unsafe metadata")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxDaemonMessageBytes+1))
	decoder.DisallowUnknownFields()
	var state activeState
	if err := decoder.Decode(&state); err != nil {
		return activeState{}, false, err
	}
	if state.SchemaVersion != 1 || !validActivePhase(state.Phase) || validateSystemdReceipt(state.Receipt) != nil {
		return activeState{}, false, errors.New("active helper state is invalid")
	}
	return state, true, nil
}

func validActivePhase(phase string) bool {
	switch phase {
	case "intent", "schedule_unverified", "scheduled", "firing", "fire_unverified", "expired":
		return true
	default:
		return false
	}
}

// Fire is the only timer callback accepted by the privileged helper binary. It
// re-opens root-owned state, checks the exact job/token/boot/deadline, and only
// then invokes the fixed system power command. An overdue timer is retained for
// manual cancel/reconcile and never powers off immediately after a long sleep.
func Fire(ctx context.Context, config Config, jobID string) error {
	if os.Geteuid() != 0 {
		return errors.New("the Linux timer callback must run as root")
	}
	config = withDefaults(config)
	if err := prepareRootDirectory(config.StateDirectory); err != nil {
		return err
	}
	return firePrepared(ctx, config, jobID)
}

func firePrepared(ctx context.Context, config Config, jobID string) error {
	if err := actions.ValidateJobID(jobID); err != nil {
		return err
	}
	config = withDefaults(config)
	if config.MaxFireLateness > 5*time.Minute {
		return errors.New("maximum timer lateness exceeds the helper safety limit")
	}
	stateLock, err := acquireActiveStateLock(filepath.Join(config.StateDirectory, "active.lock"), 10*time.Second)
	if err != nil {
		return err
	}
	defer stateLock.Close()
	s := &server{config: config}
	active, found, err := s.loadActive()
	if err != nil {
		return err
	}
	if !found {
		return errors.New("timer callback has no active DoneThen job")
	}
	expectedToken := actions.SystemdUnitToken(jobID)
	if active.Phase != "scheduled" || active.Receipt.JobID != jobID || active.Receipt.ExternalToken != expectedToken {
		return errors.New("timer callback does not match the scheduled DoneThen job")
	}
	currentBootID := linuxBootID()
	if currentBootID == "" || active.Receipt.BootID == "" || currentBootID != active.Receipt.BootID {
		active.Phase = "fire_unverified"
		_ = s.saveActive(active)
		return errors.New("timer callback boot identity changed; keeping the machine on")
	}
	now := config.Now().UTC()
	if now.Before(active.Receipt.Deadline) {
		active.Phase = "fire_unverified"
		_ = s.saveActive(active)
		return errors.New("timer callback arrived before its persisted deadline")
	}
	if now.Sub(active.Receipt.Deadline) > config.MaxFireLateness {
		active.Phase = "expired"
		if err := s.saveActive(active); err != nil {
			return err
		}
		return errors.New("timer callback exceeded maximum lateness; keeping the machine on")
	}
	active.Phase = "firing"
	if err := s.saveActive(active); err != nil {
		return err
	}
	exitCode, _, runErr := config.Runner.Run(ctx, config.SystemctlPath, "poweroff")
	if runErr != nil || exitCode != 0 {
		active.Phase = "fire_unverified"
		_ = s.saveActive(active)
		if runErr == nil {
			runErr = fmt.Errorf("systemctl exited with code %d", exitCode)
		}
		return fmt.Errorf("request system poweroff: %w", runErr)
	}
	return nil
}

func decodeDaemonRequest(reader io.Reader) (daemonRequest, error) {
	buffered := bufio.NewReader(io.LimitReader(reader, maxDaemonMessageBytes+1))
	line, err := buffered.ReadBytes('\n')
	if err != nil {
		return daemonRequest{}, err
	}
	if len(line) > maxDaemonMessageBytes {
		return daemonRequest{}, errors.New("helper request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var request daemonRequest
	if err := decoder.Decode(&request); err != nil {
		return daemonRequest{}, err
	}
	return request, nil
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *syscall.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if controlErr != nil || credentials == nil {
		return 0, controlErr
	}
	return credentials.Uid, nil
}

func prepareRootDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("helper state directory must be absolute")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("helper state directory has unsafe metadata")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("helper state directory must be root-owned")
	}
	return nil
}

func prepareSocketPath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("helper socket path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to replace an unsafe helper socket path")
	}
	return os.Remove(path)
}

func secureSocket(path, groupName string) error {
	group, err := user.LookupGroup(groupName)
	if err != nil {
		return fmt.Errorf("look up helper group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse helper group id: %w", err)
	}
	if err := os.Chown(path, 0, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0o660)
}

func acquireDaemonLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another DoneThen power helper is already running")
	}
	return file, nil
}

func acquireActiveStateLock(path string, timeout time.Duration) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if err := filetrust.ValidateOwnerControlled(path, info, "helper active-state lock"); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filetrust.HardenOwnerControlled(path); err != nil {
		_ = file.Close()
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, errors.New("helper active-state lock timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func linuxBootID() string {
	value, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

func failure(code, message string) daemonResponse {
	if len(message) > 256 {
		message = message[:256]
	}
	return daemonResponse{SchemaVersion: daemonProtocolVersion, OK: false, ErrorCode: code, Message: message}
}
