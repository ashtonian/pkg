//go:build integration && (linux || darwin)

package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCancellationStopsOwnedDescendants(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()); require.NoError(t, writer.Close()) })
	require.NoError(t, reader.SetReadDeadline(time.Now().Add(10*time.Second)))
	supervisor, err := New(WithTimeout(20*time.Second), WithGrace(50*time.Millisecond))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := supervisor.Run(ctx, Command{Name: "/bin/sh", Args: []string{"-c", `trap '' TERM; sleep 300 & child=$!; printf '%s %s\n' "$$" "$child" >&3; wait`}, ExtraFiles: []*os.File{writer}})
		done <- err
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	require.NoError(t, err)
	fields := strings.Fields(line)
	require.Len(t, fields, 2)
	parent, err := strconv.Atoi(fields[0])
	require.NoError(t, err)
	child, err := strconv.Atoi(fields[1])
	require.NoError(t, err)
	t.Cleanup(func() {
		err := syscall.Kill(-parent, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}
	})
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-t.Context().Done():
		t.Fatal("subprocess supervision did not return")
	}
	inspect, cancelInspect := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelInspect()
	output, inspectErr := exec.CommandContext(inspect, "ps", "-p", strconv.Itoa(child), "-o", "stat=").Output()
	if inspectErr != nil {
		require.NoError(t, inspect.Err())
		var exit *exec.ExitError
		require.ErrorAs(t, inspectErr, &exit)
		require.Equal(t, 1, exit.ExitCode())
	} else {
		require.True(t, strings.HasPrefix(strings.TrimSpace(string(output)), "Z"), fmt.Sprintf("child remains active: %s", output))
	}
}
func TestOutputLimitAndFailure(t *testing.T) {
	supervisor, err := New(WithTimeout(time.Second), WithGrace(50*time.Millisecond), WithMaxOutputBytes(4))
	require.NoError(t, err)
	result, err := supervisor.Run(t.Context(), Command{Name: "/bin/sh", Args: []string{"-c", "printf 123456789"}})
	require.ErrorIs(t, err, ErrOutputLimit)
	require.Equal(t, "1234", string(result.Stdout))
	_, err = supervisor.Run(t.Context(), Command{Name: "/bin/sh", Args: []string{"-c", "exit 7"}})
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 7, exit.ExitCode())
}

func TestOutputLimitStopsACommandWaitingForDescendants(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()); require.NoError(t, writer.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, reader.SetReadDeadline(time.Now().Add(5*time.Second)))
	supervisor, err := New(WithTimeout(20*time.Second), WithGrace(20*time.Millisecond), WithMaxOutputBytes(4))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := supervisor.Run(ctx, Command{Name: "/bin/sh", Args: []string{"-c", `trap '' TERM PIPE; sleep 300 & child=$!; printf '%s\n' "$$" >&3; printf 123456789; wait`}, ExtraFiles: []*os.File{writer}})
		done <- err
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	require.NoError(t, err)
	parent, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err)
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		err := syscall.Kill(-parent, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Error(err)
		}
	})
	err = <-done
	finished = true
	require.ErrorIs(t, err, ErrOutputLimit)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "capture failure must stop owned work before the outer execution deadline")
}
