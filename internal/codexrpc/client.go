package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
)

// Client serializes request/response exchanges on one owned duplex stream.
// Closing the supplied stream must unblock both Read and Write.
type Client struct {
	stream      io.ReadWriteCloser
	reader      *bufio.Reader
	gate        chan struct{}
	closed      chan struct{}
	once        sync.Once
	sequence    int64
	toolHandler ToolHandler
	observer    func(Message) error
	constraint  *threadConstraint
}

type threadConstraint struct {
	profile          runtime.Profile
	directory        string
	dynamicToolsHash string
	expires          time.Time
	threadID         string
}

// ToolHandler services only item/tool/call requests. It must validate the exact
// tool, arguments and run identity and durably record evidence before returning.
// It must respect cancellation and must not reenter this Client.
type ToolHandler func(context.Context, json.RawMessage) (json.RawMessage, error)

// NewWithToolHandler installs one immutable scoped dynamic-tool handler.
// Other server methods, including approval requests, remain denied.
func NewWithToolHandler(stream io.ReadWriteCloser, handler ToolHandler) *Client {
	c := New(stream)
	c.toolHandler = handler
	return c
}

// New takes ownership of a stream; it starts no subprocess or network connection.
func New(stream io.ReadWriteCloser) *Client {
	return &Client{stream: stream, reader: bufio.NewReaderSize(stream, MaxMessage+1), gate: make(chan struct{}, 1), closed: make(chan struct{})}
}

// ConstrainThread freezes the only profile, workspace and dynamic-tool set
// admitted on this connection. The snapshot is canonical and caller mutation
// after this call cannot widen authority.
func (c *Client) ConstrainThread(profile runtime.Profile, directory string, tools []any, expires time.Time) error {
	if c.constraint != nil || profile.Validate() != nil || directory == "" || expires.IsZero() || !expires.After(time.Now().UTC()) {
		return errors.New("invalid or duplicate Codex thread constraint")
	}
	hash, err := canonical.Hash("harness.codex-dynamic-tools.v1", tools)
	if err != nil {
		return err
	}
	c.constraint = &threadConstraint{profile: profile, directory: directory, dynamicToolsHash: hash, expires: expires}
	return nil
}

func (c *Client) validateThreadConstraint(profile runtime.Profile, directory string, tools []any) error {
	if c.constraint == nil {
		return nil
	}
	if !c.constraint.expires.After(time.Now().UTC()) {
		return errors.New("Codex capability attestation expired")
	}
	hash, err := canonical.Hash("harness.codex-dynamic-tools.v1", tools)
	if err != nil {
		return err
	}
	if profile != c.constraint.profile || directory != c.constraint.directory || hash != c.constraint.dynamicToolsHash {
		return errors.New("Codex thread differs from capability attestation")
	}
	return nil
}

func (c *Client) validateContinuationConstraint(profile runtime.Profile, directory, dynamicToolsHash string) error {
	if c.constraint == nil {
		return nil
	}
	if !c.constraint.expires.After(time.Now().UTC()) || profile != c.constraint.profile || directory != c.constraint.directory || dynamicToolsHash != c.constraint.dynamicToolsHash {
		return errors.New("Codex continuation differs from capability attestation")
	}
	return nil
}

func (c *Client) bindConstrainedThread(threadID string) error {
	if c.constraint == nil {
		return nil
	}
	if threadID == "" || c.constraint.threadID != "" && c.constraint.threadID != threadID {
		return errors.New("Codex thread differs from capability attestation")
	}
	c.constraint.threadID = threadID
	return nil
}

func (c *Client) rawCallDenied(method string) bool {
	if c.constraint == nil {
		return false
	}
	switch method {
	case "initialize", "config/read", "experimentalFeature/list", "mcpServerStatus/list", "account/login/start":
		return false
	default:
		return true
	}
}

// SetObserver installs an incoming evidence observer before execution. It must
// not reenter Client. Returning an error stops the exchange before tool replies.
func (c *Client) SetObserver(observer func(Message) error) { c.observer = observer }

// InterruptTarget is implemented by an observer stop requiring a best-effort
// interrupt. The observer must journal interrupt intent before returning it.
type InterruptTarget interface{ InterruptTarget() (string, string) }

func (c *Client) observe(m Message) error {
	if c.observer == nil {
		return nil
	}
	err := c.observer(m)
	if err != nil {
		var target InterruptTarget
		if errors.As(err, &target) {
			thread, turn := target.InterruptTarget()
			params, _ := json.Marshal(map[string]string{"threadId": thread, "turnId": turn})
			c.sequence++
			// No acknowledgement is claimed. operate closes the transport after this.
			_ = c.write(Message{ID: json.RawMessage(strconv.FormatInt(c.sequence, 10)), Method: "turn/interrupt", Params: params})
		}
	}
	return err
}

// Close invalidates the connection and unblocks an active exchange. No reconnect occurs.
func (c *Client) Close() error {
	var err error
	c.once.Do(func() { close(c.closed); err = c.stream.Close() })
	return err
}

func (c *Client) operate(ctx context.Context, operation func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errors.New("provider connection closed")
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.closed:
		return errors.New("provider connection closed")
	default:
	}
	done := make(chan error, 1)
	go func() { done <- operation() }()
	select {
	case err := <-done:
		if err != nil {
			_ = c.Close()
		}
		return err
	case <-ctx.Done():
		_ = c.Close()
		<-done
		return ctx.Err()
	}
}

func (c *Client) write(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err = Decode(b); err != nil {
		return err
	}
	b = append(b, '\n')
	for len(b) > 0 {
		n, err := c.stream.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func (c *Client) read() (Message, error) {
	b, err := c.reader.ReadSlice('\n')
	if err != nil {
		return Message{}, err
	}
	return Decode(b[:len(b)-1])
}

func (c *Client) answer(ctx context.Context, m *Message) error {
	if m.Method == "item/tool/call" && c.toolHandler != nil {
		if c.constraint != nil {
			var scoped struct {
				ThreadID string `json:"threadId"`
			}
			if err := json.Unmarshal(m.Params, &scoped); err != nil || scoped.ThreadID == "" || scoped.ThreadID != c.constraint.threadID {
				return errors.New("foreign Codex thread tool request denied by capability attestation")
			}
		}
		result, err := c.toolHandler(ctx, m.Params)
		if err != nil {
			return err
		}
		if len(result) == 0 {
			return errors.New("empty dynamic tool result")
		}
		if err := c.write(Message{ID: m.ID, Result: result}); err != nil {
			return err
		}
		m.toolHandled = true
		return nil
	}
	return c.write(Message{ID: m.ID, Error: json.RawMessage(`{"code":-32601,"message":"Harness does not authorize this server request"}`)})
}

func (c *Client) next(ctx context.Context, notifications *[]Message, consumed *int) (Message, error) {
	for n := 0; n < 256; n++ {
		m, err := c.read()
		if err != nil {
			return m, err
		}
		if err := c.observe(m); err != nil {
			return m, err
		}
		*consumed += len(m.Params) + len(m.Result) + len(m.Error) + len(m.Method) + len(m.ID)
		if *consumed > 4*MaxMessage {
			return m, errors.New("provider exchange exceeds byte budget")
		}
		if m.Method == "" {
			return m, nil
		}
		if len(*notifications) >= 256 {
			return m, errors.New("provider exchange exceeds event budget")
		}
		if len(m.ID) != 0 {
			if err := c.answer(ctx, &m); err != nil {
				return m, err
			}
		}
		*notifications = append(*notifications, m)
	}
	return Message{}, errors.New("provider exchange exceeds event budget")
}

// Call sends once, returns the matching result envelope and bounded intervening
// notifications. A response mismatch or transport failure invalidates the stream.
// A provider error remains a response; it never triggers a retry.
func (c *Client) Call(ctx context.Context, method string, params any) (response Message, notifications []Message, err error) {
	return c.call(ctx, method, params, false)
}

func (c *Client) callConstrained(ctx context.Context, method string, params any) (response Message, notifications []Message, err error) {
	return c.call(ctx, method, params, true)
}

func (c *Client) call(ctx context.Context, method string, params any, constrainedLifecycleValidated bool) (response Message, notifications []Message, err error) {
	notifications = []Message{}
	if c.rawCallDenied(method) && !constrainedLifecycleValidated {
		return response, notifications, errors.New("raw Codex method denied by capability attestation")
	}
	err = c.operate(ctx, func() error {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		c.sequence++
		id := json.RawMessage(strconv.FormatInt(c.sequence, 10))
		if err := c.write(Message{ID: id, Method: method, Params: b}); err != nil {
			return err
		}
		consumed := 0
		response, err = c.next(ctx, &notifications, &consumed)
		if err != nil {
			return err
		}
		if string(response.ID) != string(id) {
			return errors.New("provider response ID mismatch")
		}
		return nil
	})
	return
}

// Notify sends a single client notification without waiting for a response.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if c.constraint != nil && method != "initialized" {
		return errors.New("raw Codex notification denied by capability attestation")
	}
	return c.operate(ctx, func() error {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return c.write(Message{Method: method, Params: b})
	})
}

// Receive reads one unsolicited event after a completed request. Server requests
// are returned as evidence after a scoped tool response or explicit denial.
func (c *Client) Receive(ctx context.Context) (message Message, err error) {
	err = c.operate(ctx, func() error {
		var err error
		message, err = c.read()
		if err != nil {
			return err
		}
		if err := c.observe(message); err != nil {
			return err
		}
		if message.Method == "" {
			return errors.New("unsolicited provider response")
		}
		if len(message.ID) != 0 {
			return c.answer(ctx, &message)
		}
		return nil
	})
	return
}
