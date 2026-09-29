package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/logs"
	"github.com/brymartinez/foreman/internal/task"
)

// logLines is how much of a runner log the dashboard keeps, so page up reaches back
// further than one screen.
const logLines = 500

type Options struct {
	In     *os.File
	Out    *os.File
	Source Source
	// GitHubEvery is how often the searches, the gate, and the cleanup run.
	GitHubEvery time.Duration
	// LocalEvery is how often the task files and the selected log are re-read.
	LocalEvery time.Duration
}

// result is one finished background call. The event loop owns the model, so a
// goroutine never writes to it.
type result struct {
	report  *app.InboxReport
	message string
	err     error
}

// Run draws the dashboard on o.Out until the user quits or ctx ends. It puts the
// terminal in raw mode and restores it, so the caller must have checked that
// o.In and o.Out are terminals.
func Run(ctx context.Context, o Options) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state, err := term.MakeRaw(int(o.In.Fd()))
	if err != nil {
		return fmt.Errorf("put the terminal in raw mode: %w", err)
	}
	defer func() {
		// Restore the terminal even on a panic, so the shell stays usable.
		fmt.Fprint(o.Out, showCursor+leaveAlt)
		if restoreErr := term.Restore(int(o.In.Fd()), state); err == nil {
			err = restoreErr
		}
	}()
	fmt.Fprint(o.Out, enterAlt+hideCursor+clear)

	m := Model{Message: "Reading task files…"}
	m.Width, m.Height = size(o.Out)

	keys := make(chan Key, 64)
	go readKeys(ctx, o.In, keys)
	results := make(chan result, 8)

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)

	local := time.NewTicker(o.LocalEvery)
	defer local.Stop()
	remote := time.NewTicker(o.GitHubEvery)
	defer remote.Stop()

	refresh := func() {
		if m.Refreshing {
			return
		}
		m.Refreshing = true
		go func() {
			report, err := o.Source.Inbox(ctx)
			results <- result{report: &report, err: err}
		}()
	}
	refresh()
	readLocal(ctx, o.Source, &m)
	draw(o.Out, Frame(m))

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-resize:
			m.Width, m.Height = size(o.Out)
			fmt.Fprint(o.Out, clear)
		case <-local.C:
			readLocal(ctx, o.Source, &m)
		case <-remote.C:
			refresh()
		case r := <-results:
			apply(&m, r)
		case k := <-keys:
			switch a := m.Key(k); a.Kind {
			case ActionQuit:
				return nil
			case ActionRefresh:
				refresh()
			case ActionStart:
				req := a.Start
				go func() { results <- start(ctx, o.Source, req) }()
			case ActionCancel:
				rec := a.Cancel
				go func() { results <- stop(ctx, o.Source, rec) }()
			}
		}
		draw(o.Out, Frame(m))
	}
}

func apply(m *Model, r result) {
	if r.report != nil {
		m.Refreshing = false
		m.RefreshedAt = time.Now()
		if r.err == nil {
			m.SetInbox(app.Delegable(*r.report))
			m.Warnings = r.report.Warnings
		}
	}
	switch {
	case r.err != nil:
		m.Message = "! " + r.err.Error()
	case r.message != "":
		m.Message = r.message
	}
}

func start(ctx context.Context, s Source, req app.StartRequest) result {
	rec, err := s.Start(ctx, req)
	if err != nil {
		return result{err: fmt.Errorf("start %s#%d: %w", req.Repo, req.Issue, err)}
	}
	if rec.State == task.Failed {
		return result{message: fmt.Sprintf("! %s#%d could not start: %s", rec.Repo, rec.Number, rec.LastOutcome.Summary)}
	}
	return result{message: fmt.Sprintf("Started %s#%d on %s", rec.Repo, rec.Number, rec.Delegation.Branch)}
}

func stop(ctx context.Context, s Source, rec *task.Record) result {
	updated, err := s.Cancel(ctx, rec)
	if err != nil {
		return result{err: fmt.Errorf("cancel %s#%d: %w", rec.Repo, rec.Number, err)}
	}
	return result{message: fmt.Sprintf("Cancelled %s#%d; the branch and worktree are kept", updated.Repo, updated.Number)}
}

// readLocal re-reads the task files and the selected task's log. Both are local, so
// this runs on the event loop rather than in a goroutine.
func readLocal(ctx context.Context, s Source, m *Model) {
	recs, err := s.Tasks(ctx)
	if err != nil {
		m.Message = "! " + err.Error()
		return
	}
	m.SetTasks(recs)
	m.Log, m.LogErr = nil, ""
	rec := m.Selected()
	if rec == nil || rec.Delegation == nil || rec.Delegation.RunnerLog == "" {
		return
	}
	lines, err := tailFile(rec.Delegation.RunnerLog)
	if err != nil {
		m.LogErr = err.Error()
		return
	}
	m.Log = lines
}

func tailFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return logs.Tail(f, logLines, false)
}

// readKeys decodes stdin into whole keys until stdin ends. A read in progress when
// Run returns cannot be interrupted, so the caller must exit soon after.
func readKeys(ctx context.Context, in io.Reader, out chan<- Key) {
	buf := make([]byte, 0, 64)
	chunk := make([]byte, 64)
	for {
		n, err := in.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			for {
				k, used, ok := DecodeKey(buf)
				if !ok {
					break
				}
				buf = buf[used:]
				select {
				case out <- k:
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

const (
	enterAlt   = "\x1b[?1049h"
	leaveAlt   = "\x1b[?1049l"
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	clear      = "\x1b[2J"
	home       = "\x1b[H"
	eraseLine  = "\x1b[K"
)

// draw repaints the whole screen. At one frame a second a full repaint inside the
// alternate screen costs nothing and cannot leave stale text behind.
func draw(w io.Writer, lines []string) {
	var b strings.Builder
	b.WriteString(home)
	for i, l := range lines {
		if i > 0 {
			// Only between lines: a newline after the last one scrolls the screen.
			b.WriteString("\r\n")
		}
		b.WriteString(l)
		b.WriteString(eraseLine)
	}
	fmt.Fprint(w, b.String())
}

// size reports the terminal's size, falling back to a common default when the
// ioctl fails, which keeps the dashboard drawable rather than blank.
func size(out *os.File) (int, int) {
	w, h, err := term.GetSize(int(out.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}
