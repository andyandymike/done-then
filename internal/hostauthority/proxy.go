package hostauthority

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/andyandymike/done-then/internal/processgroup"
)

type Proxy struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	client  *Client
	group   processgroup.Group
	wait    chan error
	close   sync.Once
}

func StartProxy(ctx context.Context, codexExecutable, version string, stderr io.Writer) (*Proxy, error) {
	return StartProxyWithArgs(ctx, codexExecutable, nil, version, stderr)
}

func StartProxyWithArgs(ctx context.Context, codexExecutable string, prefixArgs []string, version string, stderr io.Writer) (*Proxy, error) {
	if codexExecutable == "" {
		return nil, errors.New("Codex executable is required")
	}
	args := append(append([]string(nil), prefixArgs...), "app-server", "proxy")
	command := exec.CommandContext(ctx, codexExecutable, args...)
	configureProxyProcess(command)
	if err := processgroup.Prepare(command); err != nil {
		return nil, fmt.Errorf("prepare app-server proxy process tree: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open app-server proxy stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open app-server proxy stdout: %w", err)
	}
	if stderr == nil {
		stderr = io.Discard
	}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start app-server proxy: %w", err)
	}
	group, err := processgroup.Attach(command.Process)
	if err != nil {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("isolate app-server proxy process tree: %w", err)
	}
	proxy := &Proxy{
		command: command,
		stdin:   stdin,
		client:  NewClient(stdout, stdin),
		group:   group,
		wait:    make(chan error, 1),
	}
	go func() { proxy.wait <- command.Wait() }()
	initializeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := proxy.client.Initialize(initializeCtx, "done_then", version, true); err != nil {
		_ = proxy.Close()
		return nil, fmt.Errorf("initialize app-server proxy: %w", err)
	}
	return proxy, nil
}

func (p *Proxy) Client() *Client { return p.client }

func (p *Proxy) Close() error {
	var closeErr error
	p.close.Do(func() {
		_ = p.stdin.Close()
		select {
		case err := <-p.wait:
			closeErr = err
		case <-time.After(2 * time.Second):
			if p.group != nil {
				closeErr = p.group.Close()
				p.group = nil
			} else if p.command.Process != nil {
				closeErr = p.command.Process.Kill()
			}
			<-p.wait
		}
		if p.group != nil {
			groupErr := p.group.Close()
			p.group = nil
			if closeErr == nil {
				closeErr = groupErr
			}
		}
	})
	return closeErr
}
