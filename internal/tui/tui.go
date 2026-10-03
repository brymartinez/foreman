package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/brymartinez/foreman/internal/agent"
	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/logs"
	"github.com/brymartinez/foreman/internal/task"
)

// logChunk bounds one read of a runner log: the tail read when a log opens, and each
// read of older lines as the reader scrolls back. A task that runs for hours writes far
// more than a pane can show. Tests shrink it.
var logChunk int64 = 128 << 10

type Options struct {
	In     *os.File
	Out    *os.File
	Source Source
	// GitHubEvery is how often the searches, the gate, and the cleanup run.
	GitHubEvery time.Duration
	// LocalEvery is how often the task files and the selected log are re-read.
	LocalEvery time.Duration
	Auto       Auto
	// Route says routing.yaml is in use, even one that failed to load: Route then
	// reports the error rather than starting with the wrong agent.
	Route bool
	// Version is the foreman version the help shows.
	Version string
}

// result is one finished background call. The event loop owns the model, so a
// goroutine never writes to it.
type result struct {
	report  *app.InboxReport
	message string
	err     error
	// finished marks a Start, Cancel, or answer coming back, which quitting waits for. A
	// refresh does not write anything, so it is safe to abandon.
	finished bool
	// key is the issue a start or resume was for, so its auto mode slot can be freed.
	key task.Key
	// auto marks a start auto mode made rather than the developer.
	auto bool
	// respond marks an answer coming back; answered is the record it left.
	respond  bool
	answered *task.Record
	// allRepos is the view a refresh fetched.
	allRepos bool
	// routed is a manual route's result, for the request numbered routeSeq.
	routed   *app.Decision
	routeSeq int
	// wait is why routing held an auto start back; selection is what it chose.
	wait      string
	selection agent.Selection
}

// staleView reports a refresh fetched for the view the developer has since left.
func (r result) staleView(m *Model) bool {
	return r.report != nil && r.allRepos != m.AllRepos
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

	// The message line starts empty and only ever holds a result or an error. The
	// header carries the loading state, so there is no first message to go stale.
	var m Model
	m.Width, m.Height = size(o.Out)
	m.SetAuto(o.Auto)
	m.RouteOn, m.Version = o.Route, o.Version

	keys := make(chan Key, 64)
	go readKeys(ctx, o.In, keys, escapeWait)
	results := make(chan result, 8)
	// pending counts Start, Cancel, and Respond calls in flight. Quitting waits for
	// them: App.Start records WORKING before it spawns the runner, so exiting in that
	// window would leave a task with no runner that nothing reaps.
	pending := 0

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
		allRepos := m.AllRepos
		go func() {
			report, err := o.Source.InboxView(ctx, allRepos)
			results <- result{report: &report, err: err, allRepos: allRepos}
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
			// A run that left WORKING frees its slot here, without waiting for GitHub.
			pending += launchAuto(ctx, o.Source, &m, results)
		case <-remote.C:
			refresh()
		case r := <-results:
			if r.finished {
				pending--
			}
			stale := r.staleView(&m)
			pending += settle(ctx, o.Source, &m, r, results)
			if stale {
				// The toggle came while this refresh ran, so fetch the new view now.
				refresh()
			}
		case k := <-keys:
			before := m.Selected()
			wasOpen := m.LogOpen
			switch a := m.Key(k); a.Kind {
			case ActionQuit:
				return finish(ctx, o.Out, &m, results, pending)
			case ActionRefresh, ActionToggleView:
				refresh()
			case ActionStart:
				req := a.Start
				pending++
				go func() { results <- start(ctx, o.Source, req, false) }()
			case ActionRoute:
				req, seq := a.Route, a.RouteSeq
				go func() { results <- route(ctx, o.Source, req, seq) }()
			case ActionCancel:
				rec := a.Cancel
				pending++
				go func() { results <- stop(ctx, o.Source, rec) }()
			case ActionResume:
				key := a.Resume
				pending++
				go func() { results <- resume(ctx, o.Source, key) }()
			case ActionRespond:
				key, raw := a.Respond, a.Answer
				pending++
				go func() { results <- respond(ctx, o.Source, key, raw) }()
			case ActionReadLog:
				readLog(&m)
			case ActionLoadOlder:
				loadOlder(&m)
			case ActionAuto:
				if o.Auto.Save != nil {
					if err := o.Auto.Save(m.AutoOn); err != nil {
						m.Message = "! save auto mode: " + err.Error()
					}
				}
				pending += launchAutoFresh(ctx, o.Source, &m, results)
			}
			if m.Selected() != before || (!wasOpen && m.LogOpen) {
				// The heading names the new task, so the body must not still hold the
				// old task's lines until the next local tick.
				readLog(&m)
			}
		}
		draw(o.Out, Frame(m))
	}
}

// finish waits for a Start or Cancel to come back before Run returns, so the process
// never exits between App.Start persisting WORKING and the runner being spawned.
func finish(ctx context.Context, out io.Writer, m *Model, results <-chan result, pending int) error {
	for pending > 0 {
		m.Message = "Finishing the last command…"
		draw(out, Frame(*m))
		select {
		case <-ctx.Done():
			return nil
		case r := <-results:
			if r.finished {
				pending--
				m.Done(r.key)
			}
			apply(m, r)
		}
	}
	return nil
}

// settle applies a background result and returns how many auto starts it launched.
// A failed refresh starts nothing.
func settle(ctx context.Context, s Source, m *Model, r result, results chan<- result) int {
	if r.finished {
		m.Done(r.key)
	}
	stale := r.staleView(m)
	apply(m, r)
	if r.respond {
		// The last answer opens the log, so it is read now rather than on the next tick.
		readLog(m)
	}
	if !r.finished && (r.report == nil || r.err != nil || stale) {
		return 0
	}
	return launchAutoFresh(ctx, s, m, results)
}

// launchAutoFresh re-reads the task files before auto mode counts slots. A start
// that just came back, or a run another foreman command started during a refresh,
// is then counted, so auto mode never goes past its limit on a stale task list.
func launchAutoFresh(ctx context.Context, s Source, m *Model, results chan<- result) int {
	if !m.AutoOn {
		return 0
	}
	readLocal(ctx, s, m)
	return launchAuto(ctx, s, m, results)
}

// launchAuto starts what auto mode picks, each in its own goroutine like a manual
// start, and returns how many it launched so quitting waits for them.
func launchAuto(ctx context.Context, s Source, m *Model, results chan<- result) int {
	reqs := m.AutoStarts()
	for _, req := range reqs {
		prior := m.prior(task.Key{Repo: req.Repo, Number: req.Issue})
		go func() { results <- autoStart(ctx, s, req, prior) }()
	}
	return len(reqs)
}

func apply(m *Model, r result) {
	if r.staleView(m) {
		m.Refreshing = false
		return
	}
	if r.routed != nil {
		m.Routed(r.routeSeq, *r.routed, r.err)
		return
	}
	if r.report != nil {
		m.Refreshing = false
		m.RefreshedAt = time.Now()
		if r.err == nil {
			m.SetInbox(app.Delegable(*r.report))
			m.Warnings = r.report.Warnings
			m.retryWaiting()
		}
	}
	if r.auto && r.finished {
		if r.wait != "" {
			m.autoWaiting(r.key, r.wait, r.selection)
		} else {
			delete(m.waiting, r.key)
		}
	}
	if r.respond {
		if r.err != nil {
			m.AnswerFailed()
		} else {
			m.Answered(r.answered)
		}
	}
	switch {
	case r.err != nil:
		m.Message = "! " + r.err.Error()
	case r.auto && !strings.HasPrefix(r.message, "!") && strings.HasPrefix(m.Message, "!"):
		// Auto starts land in bursts. A failed one stays on screen while the rest
		// succeed; the task list shows those.
	case r.message != "":
		m.Message = r.message
	}
}

func start(ctx context.Context, s Source, req app.StartRequest, auto bool) result {
	r := startResult(ctx, s, req)
	r.key, r.auto, r.finished = task.Key{Repo: req.Repo, Number: req.Issue}, auto, true
	if !auto {
		return r
	}
	if r.err != nil {
		r.err = fmt.Errorf("auto: %w", r.err)
	} else if msg, ok := strings.CutPrefix(r.message, "! "); ok {
		r.message = "! auto: " + msg
	} else {
		r.message = "auto: " + r.message
	}
	return r
}

// route asks App.Route for a manual start's profile. It writes nothing, so it is not
// counted as pending.
func route(ctx context.Context, s Source, req app.RouteRequest, seq int) result {
	d, err := s.Route(ctx, req)
	if err != nil {
		err = fmt.Errorf("route %s#%d: %w", req.Repo, req.Issue, err)
	}
	return result{routed: &d, routeSeq: seq, err: err}
}

// autoStart routes an auto start that has no agent, then starts it with the chosen
// profile. When no suitable profile can run, nothing starts and the result says why.
func autoStart(ctx context.Context, s Source, req app.StartRequest, prior *agent.Selection) result {
	if req.Agent != "" {
		return start(ctx, s, req, true)
	}
	key := task.Key{Repo: req.Repo, Number: req.Issue}
	d, err := s.Route(ctx, app.RouteRequest{Repo: req.Repo, Issue: req.Issue, Prior: prior})
	switch {
	case err != nil:
		return result{err: fmt.Errorf("auto: route %s#%d: %w", req.Repo, req.Issue, err), key: key, auto: true, finished: true}
	case d.Profile == nil:
		return result{message: fmt.Sprintf("! auto: %s#%d waits: %s", req.Repo, req.Issue, d.Wait), wait: d.Wait, selection: d.Selection, key: key, auto: true, finished: true}
	}
	req.Profile, req.RouteReason = d.Profile, d.Reason
	return start(ctx, s, req, true)
}

func startResult(ctx context.Context, s Source, req app.StartRequest) result {
	if req.ContextFile != "" {
		// The runner a later `foreman respond` spawns reads this path from its own
		// working directory, so store it absolute, as `inbox` and `start` do.
		abs, err := filepath.Abs(req.ContextFile)
		if err != nil {
			return result{err: fmt.Errorf("context file %s: %w", req.ContextFile, err), finished: true}
		}
		req.ContextFile = abs
	}
	rec, err := s.Start(ctx, req)
	if err != nil {
		return result{err: fmt.Errorf("start %s#%d: %w", req.Repo, req.Issue, err), finished: true}
	}
	if rec.State == task.Failed {
		return result{message: fmt.Sprintf("! %s#%d could not start: %s", rec.Repo, rec.Number, rec.LastOutcome.Summary), finished: true}
	}
	if p := rec.Delegation.Profile; p != nil {
		return result{message: fmt.Sprintf("Started %s#%d with profile %s on %s", rec.Repo, rec.Number, p.Name, rec.Delegation.Branch), finished: true}
	}
	return result{message: fmt.Sprintf("Started %s#%d on %s", rec.Repo, rec.Number, rec.Delegation.Branch), finished: true}
}

// resume puts a REVIEWED task back to work on the comments on its pull request. Like
// start, it is counted as pending, because App.Resume records WORKING before it spawns
// the runner.
func resume(ctx context.Context, s Source, key task.Key) result {
	r := resumeResult(ctx, s, key)
	r.key = key
	return r
}

func resumeResult(ctx context.Context, s Source, key task.Key) result {
	rec, err := s.Resume(ctx, key)
	if err != nil {
		return result{err: fmt.Errorf("resume %s#%d: %w", key.Repo, key.Number, err), finished: true}
	}
	if rec.State == task.Failed {
		return result{message: fmt.Sprintf("! %s#%d could not resume: %s", rec.Repo, rec.Number, rec.LastOutcome.Summary), finished: true}
	}
	return result{message: fmt.Sprintf("Resumed %s#%d on PR #%d", rec.Repo, rec.Number, rec.Delegation.PR), finished: true}
}

// respond records an answer typed in the dashboard through App.Respond, so it is
// resolved exactly as foreman respond resolves it. It is counted as pending, because
// the last answer records WORKING before App.Respond spawns the runner.
func respond(ctx context.Context, s Source, key task.Key, raw string) result {
	r := respondResult(ctx, s, key, raw)
	r.key, r.respond, r.finished = key, true, true
	return r
}

func respondResult(ctx context.Context, s Source, key task.Key, raw string) result {
	rec, err := s.Respond(ctx, key, raw)
	if err != nil {
		return result{err: fmt.Errorf("answer %s#%d: %w", key.Repo, key.Number, err)}
	}
	r := result{answered: rec}
	switch {
	case rec.State == task.NeedsDecision:
		n := rec.PendingCount()
		r.message = fmt.Sprintf("Recorded the answer for %s#%d; %d %s still waiting", rec.Repo, rec.Number, n, plural(n, "question", "questions"))
	case rec.State == task.Failed:
		r.message = fmt.Sprintf("! %s#%d could not resume: %s", rec.Repo, rec.Number, rec.LastOutcome.Summary)
	default:
		r.message = fmt.Sprintf("Resumed %s#%d with the answers", rec.Repo, rec.Number)
	}
	return r
}

func stop(ctx context.Context, s Source, rec *task.Record) result {
	updated, err := s.Cancel(ctx, rec)
	if err != nil {
		return result{err: fmt.Errorf("cancel %s#%d: %w", rec.Repo, rec.Number, err), finished: true}
	}
	return result{message: fmt.Sprintf("Cancelled %s#%d; the branch and worktree are kept", updated.Repo, updated.Number), finished: true}
}

// readLocal re-reads the task files and the selected task's log. Both are local, so
// this runs on the event loop rather than in a goroutine.
func readLocal(ctx context.Context, s Source, m *Model) {
	recs, err := s.Tasks(ctx)
	if err != nil {
		m.Message = "! " + err.Error()
		m.TasksFailed()
		return
	}
	m.SetTasks(recs)
	readLog(m)
}

// readLog brings the selected task's runner log up to date. It reads the tail when
// the log is new to the pane, and after that only the bytes the runner has added, so
// older lines loaded by scrolling back stay. Moving the cursor calls it too, so the
// pane never shows one task's lines under another task's heading.
func readLog(m *Model) {
	rec := m.Selected()
	if !m.LogOpen || rec == nil || rec.Delegation == nil || rec.Delegation.RunnerLog == "" {
		m.clearLog()
		return
	}
	path := rec.Delegation.RunnerLog
	if err := appendLog(m, path); err != nil {
		m.clearLog()
		m.LogErr = err.Error()
	}
}

func appendLog(m *Model, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if path != m.LogPath || size < m.logEnd {
		// A new log, or one rewritten from the start: read its tail.
		start, err := logs.Back(f, size, logChunk)
		if err != nil {
			return err
		}
		lines, next, partial, err := logs.Lines(f, start, size, m.LogRaw)
		if err != nil {
			return err
		}
		m.clearLog()
		m.Log, m.LogPath, m.LogAtStart = lines, path, start == 0
		m.logStart, m.logEnd, m.logPartial = start, next, partial
		return nil
	}
	lines, next, partial, err := logs.Lines(f, m.logEnd, size, m.LogRaw)
	if err != nil {
		return err
	}
	before := len(m.Log)
	m.Log = append(m.Log[:len(m.Log)-m.logPartial], lines...)
	m.logEnd, m.logPartial = next, partial
	if m.LogOffset > 0 {
		// The offset counts back from the tail, so a running agent's new lines would
		// slide the view forward while the reader is scrolled back. Move the offset by
		// the growth to hold the same lines on screen until End follows again.
		m.scrollLog(len(m.Log) - before)
	}
	return nil
}

// loadOlder reads chunks of the open log before its oldest loaded line until the
// scroll step that asked for them is taken or the file start is reached. Each chunk
// is bounded; g asks for the whole file. A failed read keeps what is loaded.
func loadOlder(m *Model) {
	for a := (Action{Kind: ActionLoadOlder}); a.Kind == ActionLoadOlder; {
		lines, start, err := readOlder(m.LogPath, m.logStart, m.LogRaw)
		if err != nil {
			m.logShort = 0
			m.Message = "! read older log lines: " + err.Error()
			return
		}
		a = m.LoadedOlder(lines, start)
	}
}

func readOlder(path string, end int64, raw bool) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	start, err := logs.Back(f, end, logChunk)
	if err != nil {
		return nil, 0, err
	}
	lines, _, _, err := logs.Lines(f, start, end, raw)
	return lines, start, err
}

// escapeWait is how long bytes that could still grow into a key wait for the rest. A
// terminal writes an arrow key's three bytes at once, so anything slower than this is
// the escape key itself, or a rune the terminal never finished.
const escapeWait = 40 * time.Millisecond

// readKeys decodes stdin into whole keys until stdin ends. A read in progress when Run
// returns cannot be interrupted, so the caller must exit soon after.
func readKeys(ctx context.Context, in io.Reader, out chan<- Key, wait time.Duration) {
	raw := make(chan []byte, 8)
	go readBytes(in, raw)

	var buf []byte
	// pending ticks only while buf holds bytes that are not yet a whole key.
	var pending <-chan time.Time
	// emit sends every whole key in buf and reports whether to keep reading. What it
	// leaves in buf is a key that is not finished arriving.
	emit := func(final bool) bool {
		for len(buf) > 0 {
			k, used, ok := DecodeKey(buf, final)
			if !ok {
				return true
			}
			buf = buf[used:]
			select {
			case out <- k:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-raw:
			if !ok {
				// stdin ended, so nothing more is coming: decode what is left as it is.
				emit(true)
				return
			}
			buf = append(buf, b...)
			if !emit(false) {
				return
			}
			pending = nil
			if len(buf) > 0 {
				pending = time.After(wait)
			}
		case <-pending:
			pending = nil
			if !emit(true) {
				return
			}
		}
	}
}

// readBytes copies stdin onto ch until it ends. It is its own goroutine because a
// blocking read cannot be selected on alongside the escape timer.
func readBytes(in io.Reader, ch chan<- []byte) {
	defer close(ch)
	chunk := make([]byte, 64)
	for {
		n, err := in.Read(chunk)
		if n > 0 {
			ch <- append([]byte(nil), chunk[:n]...)
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
