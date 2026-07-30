package codexexec

type Executable struct {
	Path       string
	PrefixArgs []string
}

func ResolveExecutable(configured string) (Executable, error) {
	return resolvePlatformExecutable(configured)
}
