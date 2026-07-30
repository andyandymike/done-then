package pluginstate

type stateLock interface {
	Release() error
}
