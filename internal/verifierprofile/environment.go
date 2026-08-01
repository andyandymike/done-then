package verifierprofile

import (
	"os"
	"sort"
	"strings"
)

var allowedEnvironmentKeys = map[string]bool{
	"APPDATA": true, "HOME": true, "LOCALAPPDATA": true, "PATH": true,
	"SYSTEMDRIVE": true, "SYSTEMROOT": true, "TEMP": true, "TMP": true,
	"TMPDIR": true, "USERPROFILE": true, "WINDIR": true,
	"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	"XDG_RUNTIME_DIR": true, "XDG_STATE_HOME": true,
}

func minimalEnvironment() []string {
	values := make([]string, 0)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowedEnvironmentKeys[strings.ToUpper(key)] {
			values = append(values, entry)
		}
	}
	sort.Strings(values)
	return values
}
