package pluginpower

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/andyandymike/done-then/internal/filetrust"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

type Launcher struct {
	Executable string
	DataRoot   string
}

func (l Launcher) Launch(jobID string) (int, error) {
	if err := pluginstate.ValidateJobID(jobID); err != nil {
		return 0, err
	}
	executable := l.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return 0, fmt.Errorf("resolve DoneThen executable: %w", err)
		}
	}
	if !filepath.IsAbs(executable) {
		return 0, errors.New("DoneThen supervisor executable must be absolute")
	}
	logDir := filepath.Join(l.DataRoot, "plugin", "logs")
	if err := filetrust.EnsureOwnerControlledDirectory(logDir, "supervisor log directory"); err != nil {
		return 0, err
	}
	logFile, err := filetrust.OpenAppendOwnerControlled(filepath.Join(logDir, jobID+".supervisor.log"), "supervisor log")
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	command := exec.Command(executable, "supervise", jobID)
	configureDetached(command)
	command.Env = supervisorEnvironment()
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start one-shot supervisor: %w", err)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	return pid, nil
}

func supervisorEnvironment() []string {
	keys := []string{"PATH", "HOME", "USERPROFILE", "CODEX_HOME", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL"}
	if runtime.GOOS == "windows" {
		keys = append(keys, "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH")
	} else {
		keys = append(keys, "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "DBUS_SESSION_BUS_ADDRESS")
	}
	seen := make(map[string]bool, len(keys))
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		lookupKey := key
		if runtime.GOOS == "windows" {
			lookupKey = strings.ToUpper(key)
		}
		if seen[lookupKey] {
			continue
		}
		seen[lookupKey] = true
		if value, found := os.LookupEnv(key); found {
			environment = append(environment, key+"="+value)
		}
	}
	sort.Strings(environment)
	return environment
}
