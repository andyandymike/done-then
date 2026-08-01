package hostauthority

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

const maxRPCMessageBytes = 4 << 20

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("app-server error %d: %s", e.Code, e.Message)
}

type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type rpcReply struct {
	result json.RawMessage
	err    error
}

type Caller interface {
	Call(context.Context, string, any, any) error
	Notify(string, any) error
	Notifications() <-chan Notification
	EventLossDetected() bool
}

type Client struct {
	encoder       *json.Encoder
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]chan rpcReply
	nextID        atomic.Uint64
	notifications chan Notification
	eventLoss     atomic.Bool
	closed        chan struct{}
	closeOnce     sync.Once
}

func NewClient(reader io.Reader, writer io.Writer) *Client {
	client := &Client{
		encoder:       json.NewEncoder(writer),
		pending:       make(map[string]chan rpcReply),
		notifications: make(chan Notification, 256),
		closed:        make(chan struct{}),
	}
	go client.readLoop(reader)
	return client
}

func (c *Client) Initialize(ctx context.Context, name, version string, experimental bool) error {
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    name,
			"title":   "DoneThen",
			"version": version,
		},
		"capabilities": map[string]any{"experimentalApi": experimental},
	}
	var response map[string]any
	if err := c.Call(ctx, "initialize", params, &response); err != nil {
		return err
	}
	return c.Notify("initialized", map[string]any{})
}

func (c *Client) Call(ctx context.Context, method string, params any, destination any) error {
	if method == "" {
		return errors.New("app-server method is required")
	}
	id := c.nextID.Add(1)
	key := strconv.FormatUint(id, 10)
	replies := make(chan rpcReply, 1)
	c.pendingMu.Lock()
	c.pending[key] = replies
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
	}()
	request := map[string]any{"id": id, "method": method, "params": params}
	if err := c.write(request); err != nil {
		return err
	}
	select {
	case reply := <-replies:
		if reply.err != nil {
			return reply.err
		}
		if destination == nil || len(reply.result) == 0 || string(reply.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(reply.result, destination); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errors.New("app-server connection closed")
	}
}

func (c *Client) Notify(method string, params any) error {
	if method == "" {
		return errors.New("app-server notification method is required")
	}
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *Client) Notifications() <-chan Notification { return c.notifications }
func (c *Client) EventLossDetected() bool            { return c.eventLoss.Load() }

func (c *Client) write(message any) error {
	select {
	case <-c.closed:
		return errors.New("app-server connection closed")
	default:
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.encoder.Encode(message); err != nil {
		return fmt.Errorf("write app-server message: %w", err)
	}
	return nil
}

func (c *Client) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxRPCMessageBytes)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			c.failPending(fmt.Errorf("decode app-server message: %w", err))
			c.closeOnce.Do(func() { close(c.closed) })
			close(c.notifications)
			return
		}
		if len(message.ID) != 0 {
			key := string(message.ID)
			c.pendingMu.Lock()
			channel := c.pending[key]
			c.pendingMu.Unlock()
			if channel != nil {
				if message.Error != nil {
					channel <- rpcReply{err: message.Error}
				} else {
					channel <- rpcReply{result: message.Result}
				}
			}
			continue
		}
		if message.Method == "" {
			c.eventLoss.Store(true)
			continue
		}
		notification := Notification{Method: message.Method, Params: message.Params}
		select {
		case c.notifications <- notification:
		default:
			c.eventLoss.Store(true)
		}
	}
	if err := scanner.Err(); err != nil {
		c.failPending(fmt.Errorf("read app-server message: %w", err))
	} else {
		c.failPending(errors.New("app-server connection closed"))
	}
	c.closeOnce.Do(func() { close(c.closed) })
	close(c.notifications)
}

func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for _, channel := range c.pending {
		select {
		case channel <- rpcReply{err: err}:
		default:
		}
	}
}
