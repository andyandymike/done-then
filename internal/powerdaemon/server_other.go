//go:build !linux

package powerdaemon

import "context"

func Run(context.Context, Config) error          { return ErrUnsupported }
func Fire(context.Context, Config, string) error { return ErrUnsupported }
