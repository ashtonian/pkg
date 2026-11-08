// Package process supervises bounded one-shot operator subprocesses.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// ErrOutputLimit indicates a subprocess exceeded its capture bound.
var ErrOutputLimit = errors.New("subprocess output exceeds capture limit")

// ErrUnsupportedPlatform indicates process-group supervision is unavailable.
var ErrUnsupportedPlatform = errors.New("subprocess supervision requires Linux or macOS")

// Command declares a one-shot process. Caller-owned ExtraFiles are not closed by Run.
type Command struct {
	Name       string
	Args       []string
	Input      []byte
	Dir        string
	Env        []string
	ExtraFiles []*os.File
}

// Result includes bounded output and the process identity; outputs are never logged automatically.
type Result struct {
	PID            int
	Stdout, Stderr []byte
}

// Supervisor owns timeout, process-group cleanup and output bounds for each Run.
type Supervisor struct {
	timeout, grace time.Duration
	maxBytes       int
}

// Option changes a supervisor's validated execution policy.
type Option func(*Supervisor)

// WithTimeout sets the total command deadline.
func WithTimeout(timeout time.Duration) Option { return func(s *Supervisor) { s.timeout = timeout } }

// WithGrace bounds TERM cleanup before killing the owned process group.
func WithGrace(grace time.Duration) Option { return func(s *Supervisor) { s.grace = grace } }

// WithMaxOutputBytes bounds each captured output stream.
func WithMaxOutputBytes(limit int) Option { return func(s *Supervisor) { s.maxBytes = limit } }

// New validates the policy. Defaults are 30s execution, 2s cleanup and 1 MiB per stream.
func New(options ...Option) (*Supervisor, error) {
	s := &Supervisor{timeout: 30 * time.Second, grace: 2 * time.Second, maxBytes: 1 << 20}
	for _, option := range options {
		option(s)
	}
	if s.timeout <= 0 || s.grace <= 0 || s.maxBytes <= 0 || s.maxBytes > 64<<20 {
		return nil, errors.New("subprocess deadlines and capture limits must be positive and bounded")
	}
	return s, nil
}

// Run returns after waiting for the parent, closing its pipes and killing remaining group members.
// Commands must not detach from their process group; remote actions require separate reconciliation.
func (s *Supervisor) Run(ctx context.Context, command Command) (Result, error) {
	var result Result
	if command.Name == "" {
		return result, errors.New("subprocess executable is required")
	}
	work, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(work, command.Name, command.Args...)
	cmd.Dir, cmd.Env, cmd.ExtraFiles = command.Dir, command.Env, command.ExtraFiles
	cmd.Stdin = bytes.NewReader(command.Input)
	cmd.WaitDelay = s.grace
	if err := superviseGroup(cmd); err != nil {
		return result, err
	}
	stdout, stderr := &capture{max: s.maxBytes, stop: cancel}, &capture{max: s.maxBytes, stop: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start %s: %w", command.Name, err)
	}
	result.PID = cmd.Process.Pid
	waitErr := cmd.Wait()
	cleanupErr := killGroup(result.PID)
	result.Stdout, result.Stderr = stdout.data.Bytes(), stderr.data.Bytes()
	var outputErr error
	if stdout.exceeded || stderr.exceeded {
		outputErr = ErrOutputLimit
	}
	err := errors.Join(waitErr, work.Err(), cleanupErr, outputErr)
	if err != nil {
		return result, fmt.Errorf("execute %s: %w", command.Name, err)
	}
	return result, nil
}

type capture struct {
	stop     context.CancelFunc
	data     bytes.Buffer
	max      int
	exceeded bool
}

func (c *capture) Write(value []byte) (int, error) {
	remaining := c.max - c.data.Len()
	if len(value) > remaining {
		c.exceeded = true
		c.stop()
		written, err := c.data.Write(value[:remaining])
		return written, errors.Join(ErrOutputLimit, err)
	}
	return c.data.Write(value)
}

var _ io.Writer = (*capture)(nil)
