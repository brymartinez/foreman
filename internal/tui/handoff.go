package tui

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// pausable is stdin with an off switch. The dashboard's key reader sits in a read
// on the terminal; while an agent session owns the terminal that read would steal
// the session's first keystrokes. Pause returns only once no read is in progress,
// and reads wait for readable input with a short poll, so none is ever left blocked.
type pausable struct {
	f      *os.File
	mu     sync.Mutex
	paused bool
}

const pollEvery = 50 // milliseconds

func (p *pausable) Read(b []byte) (int, error) {
	for {
		p.mu.Lock()
		if p.paused {
			p.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			continue
		}
		fds := []unix.PollFd{{Fd: int32(p.f.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, pollEvery)
		if err != nil && !errors.Is(err, unix.EINTR) {
			p.mu.Unlock()
			return 0, err
		}
		if n > 0 {
			// Readable or at end of input, so this read cannot block.
			read, rerr := p.f.Read(b)
			p.mu.Unlock()
			return read, rerr
		}
		p.mu.Unlock()
	}
}

// Pause stops reads and waits for one in progress to finish.
func (p *pausable) Pause() {
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
}

// Resume lets reads continue.
func (p *pausable) Resume() {
	p.mu.Lock()
	p.paused = false
	p.mu.Unlock()
}

// handoff gives the terminal to run and takes it back. It leaves the alternate
// screen and raw mode so the session sees an ordinary terminal, then restores both
// and clears the screen for the next frame, whether or not the session failed. The
// error it returns is the session's, or the terminal's when raw mode cannot return,
// which leaves the dashboard unusable.
func handoff(o Options, in *pausable, state **term.State, run func() error) (sessionErr, termErr error) {
	in.Pause()
	fd := int(o.In.Fd())
	fmt.Fprint(o.Out, showCursor+leaveAlt)
	if err := term.Restore(fd, *state); err != nil {
		in.Resume()
		fmt.Fprint(o.Out, enterAlt+hideCursor+clear)
		return nil, fmt.Errorf("restore the terminal for the session: %w", err)
	}
	sessionErr = run()
	st, err := term.MakeRaw(fd)
	if err != nil {
		return sessionErr, fmt.Errorf("put the terminal back in raw mode: %w", err)
	}
	*state = st
	fmt.Fprint(o.Out, enterAlt+hideCursor+clear)
	in.Resume()
	return sessionErr, nil
}
