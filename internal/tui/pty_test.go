//go:build linux

package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/csy20/agt/internal/client"
	"golang.org/x/sys/unix"
)

// TestPTYSmoke drives the session through a local pseudo-terminal.
// It checks composition, focused approval, streaming, detach, and the
// restore sequences. It does not claim a person watched a physical terminal,
// a specific emulator, or a mouse.
func TestPTYSmoke(t *testing.T) {
	master, slave := openPTY(t)
	restoreFD, err := unix.Dup(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	restoreOut := os.NewFile(uintptr(restoreFD), "pty-restore")
	var buf ptyBuf
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		tmp := make([]byte, 4096)
		for {
			n, err := master.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	p := tea.NewProgram(newModel(Options{}), tea.WithInput(slave), tea.WithOutput(slave), tea.WithAltScreen())
	runDone := make(chan error, 1)
	go func() {
		_, err := p.Run()
		runDone <- err
	}()
	defer func() {
		p.Quit()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
		// Close the slave side first so the master read returns.
		// Closing the master while Read is blocked deadlocks on the file lock.
		_ = restoreOut.Close()
		_ = slave.Close()
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
		}
		_ = master.Close()
	}()

	p.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
	waitPTY(t, &buf, "Repair a failing test", 5*time.Second)
	writePTY(t, master, []byte("hello"))
	waitPTY(t, &buf, "hello", 5*time.Second)

	p.Send(eventMsg{gen: 0, ev: client.Event{
		Seq: 1, Type: "approval.requested",
		Data: json.RawMessage(`{"tool":"apply_patch","command_id":"cmd_pty","args_hash":"abcdef1234567890abcdef","reason":"write the file","path":"payments/processor.py"}`),
	}})
	waitPTY(t, &buf, "apply_patch", 5*time.Second)
	writePTY(t, master, []byte("y"))
	waitPTY(t, &buf, "helloy", 5*time.Second)
	if strings.Contains(buf.String(), "There is no run to approve") {
		t.Fatal("composer text approved the action")
	}

	writePTY(t, master, []byte{0x0f})
	waitPTY(t, &buf, "y approve", 5*time.Second)
	writePTY(t, master, []byte("y"))
	waitPTY(t, &buf, "There is no run to approve.", 5*time.Second)
	if !strings.Contains(buf.String(), "apply_patch") {
		t.Fatal("the approval disappeared when the decision could not be sent")
	}

	writePTY(t, master, []byte{0x1b})
	time.Sleep(200 * time.Millisecond)
	writePTY(t, master, []byte{'\r'})
	waitPTY(t, &buf, "agtd is not connected", 5*time.Second)
	if !strings.Contains(buf.String(), "apply_patch") {
		t.Fatal("submitting the composer cleared the approval")
	}

	p.Send(eventMsg{gen: 0, ev: client.Event{
		Seq: 2, Type: "assistant.delta",
		Data: json.RawMessage(`{"text":"Looking at the ledger"}`),
	}})
	waitPTY(t, &buf, "Looking at the ledger", 5*time.Second)

	writePTY(t, master, []byte{0x03})
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl+c did not leave the session")
	}
	waitPTY(t, &buf, "\x1b[?1049l", 3*time.Second)

	old := os.Stdout
	os.Stdout = restoreOut
	restoreTerminal()
	os.Stdout = old
	waitPTY(t, &buf, "\x1b[?25h\x1b[0m", 3*time.Second)
}

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		t.Fatalf("pty number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		t.Fatalf("open /dev/pts/%d: %v", n, err)
	}
	ws := &unix.Winsize{Row: 40, Col: 120}
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, ws)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, ws); err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatalf("winsize: %v", err)
	}
	return master, slave
}

func writePTY(t *testing.T, f *os.File, p []byte) {
	t.Helper()
	if _, err := f.Write(p); err != nil {
		t.Fatal(err)
	}
}

func waitPTY(t *testing.T, buf *ptyBuf, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pty output missing %q\n%s", want, buf.String())
}

type ptyBuf struct {
	mu sync.Mutex
	b  []byte
}

func (p *ptyBuf) Write(in []byte) {
	p.mu.Lock()
	p.b = append(p.b, in...)
	p.mu.Unlock()
}

func (p *ptyBuf) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.b)
}
