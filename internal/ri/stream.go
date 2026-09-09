package ri

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"harness.local/engorch/internal/canonical"
)

// Stream owns one pinned RI process. Calls are sequential; transport failures
// terminate the session and are never retried. Close must be called by its owner.
type Stream struct {
	gate    chan struct{}
	cancel  context.CancelFunc
	input   io.WriteCloser
	output  io.ReadCloser
	command *exec.Cmd
	done    chan struct{}
	once    sync.Once
	next    uint32
}

// OpenStream starts a pinned process with an empty inherited environment except
// Windows system and temporary-directory variables. Context bounds its lifetime.
func (c Client) OpenStream(ctx context.Context) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.validateExecutable(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(lifetime, c.Executable, "--stdio-stream")
	command.WaitDelay = time.Second
	command.Env = []string{}
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Stderr = &limited{limit: 64 << 10, cancel: cancel}
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, err
	}
	if err = command.Start(); err != nil {
		input.Close()
		output.Close()
		cancel()
		return nil, err
	}
	s := &Stream{gate: make(chan struct{}, 1), cancel: cancel, input: input, output: output, command: command, done: make(chan struct{}), next: 1}
	go func() { _ = command.Wait(); close(s.done) }()
	return s, nil
}

// Close cancels and reaps the owned process, including a call blocked on pipe IO.
func (s *Stream) Close() error {
	s.once.Do(func() { s.cancel(); _ = s.input.Close(); _ = s.output.Close() })
	<-s.done
	return nil
}

// Call exchanges one bounded canonical frame with a 30-second deadline.
// A valid operation error leaves the session usable; malformed responses close it.
func (s *Stream) Call(ctx context.Context, request any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("RI stream closed")
	}
	defer func() { <-s.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-s.done:
		return nil, errors.New("RI stream closed")
	default:
	}
	if s.next > 10000 {
		_ = s.Close()
		return nil, errors.New("RI stream request bound exceeded")
	}
	input, err := canonical.Bytes(struct {
		ID      uint32 `json:"id"`
		Request any    `json:"request"`
	}{s.next, request})
	if err != nil {
		return nil, err
	}
	type outcome struct {
		result   json.RawMessage
		err      error
		terminal bool
	}
	result := make(chan outcome, 1)
	id := s.next
	go func() {
		body, err := exchangeFrame(s.input, s.output, input)
		if err != nil {
			result <- outcome{err: err, terminal: true}
			return
		}
		value, err, terminal := decodeStreamResponse(body, id)
		result <- outcome{value, err, terminal}
	}()
	select {
	case <-ctx.Done():
		_ = s.Close()
		<-result
		return nil, ctx.Err()
	case value := <-result:
		if value.terminal {
			_ = s.Close()
		} else {
			s.next++
		}
		if err := ctx.Err(); err != nil {
			_ = s.Close()
			return nil, err
		}
		return value.result, value.err
	}
}

func exchangeFrame(input io.Writer, output io.Reader, body []byte) ([]byte, error) {
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	if n, err := input.Write(frame); err != nil {
		return nil, err
	} else if n != len(frame) {
		return nil, io.ErrShortWrite
	}
	var header [4]byte
	if _, err := io.ReadFull(output, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > canonical.MaxBytes {
		return nil, errors.New("invalid RI response frame size")
	}
	response := make([]byte, int(size))
	_, err := io.ReadFull(output, response)
	return response, err
}

func decodeStreamResponse(body []byte, id uint32) (json.RawMessage, error, bool) {
	normal, err := canonical.Normalize(body)
	if err != nil || !bytes.Equal(normal, body) {
		return nil, errors.New("noncanonical RI stream response"), true
	}
	var response struct {
		Version int             `json:"version"`
		ID      uint32          `json:"id"`
		OK      bool            `json:"ok"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *string         `json:"error,omitempty"`
	}
	if err := canonical.Decode(body, &response); err != nil {
		return nil, err, true
	}
	if response.Version != 1 || response.ID != id {
		return nil, errors.New("RI stream response identity mismatch"), true
	}
	if !response.OK {
		if response.Error == nil || *response.Error == "" || len(response.Result) != 0 {
			return nil, errors.New("invalid RI stream failure"), true
		}
		return nil, errors.New("RI rejected request: " + *response.Error), false
	}
	if response.Error != nil || len(response.Result) == 0 || response.Result[0] != '{' {
		return nil, errors.New("invalid RI stream result"), true
	}
	return response.Result, nil, false
}
