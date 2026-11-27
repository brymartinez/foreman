package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"

	"github.com/brymartinez/foreman/internal/task"
)

// Launches counts the launches a process has under way, from the saved claim to the
// spawned runner. A process about to exit stops new ones and waits for these, so it
// leaves no task claimed with no runner. The zero value is ready to use, and a nil
// Launches tracks nothing.
type Launches struct {
	mu   sync.Mutex
	held int
	// idle is set once the process stops launching, and closes when nothing is held.
	idle chan struct{}
}

// errQuitting refuses a launch in a process that is about to exit.
var errQuitting = errors.New("foreman is quitting; nothing was launched")

// hold admits one launch. The caller takes it before the update that claims the
// launch and releases it once the runner is spawned.
func (l *Launches) hold() (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.idle != nil {
		return nil, errQuitting
	}
	l.held++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.held--; l.held == 0 && l.idle != nil {
			close(l.idle)
		}
	}, nil
}

func (l *Launches) stopping() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.idle != nil
}

// StopLaunches refuses further launches. The channel it returns closes once those
// under way have their runners.
func (a App) StopLaunches() <-chan struct{} {
	l := a.Launches
	if l == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.idle == nil {
		l.idle = make(chan struct{})
		if l.held == 0 {
			close(l.idle)
		}
	}
	return l.idle
}

// launching reports a launch in progress: its launcher still runs and has not
// recorded a runner. A recorded runner holds starts off through liveRunner instead.
func launching(rec *task.Record) bool {
	d := rec.Delegation
	return d != nil && d.Launch != nil && processAlive(d.Launch.PID)
}

// claimLaunch gives cur's delegation a new launch claim held by this process and
// returns its token, which the spawned runner needs to take the task. The caller
// holds the task lock and has checked that no launch or runner is live, so a claim
// already there is one its launcher left behind.
func (a App) claimLaunch(cur *task.Record) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	cur.Delegation.Launch = &task.Launch{Token: token, PID: os.Getpid(), Since: a.Now()}
	return token, nil
}

// ownsLaunch reports whether token is the task's launch claim.
func ownsLaunch(rec *task.Record, token string) bool {
	d := rec.Delegation
	return token != "" && d != nil && d.Launch != nil && d.Launch.Token == token
}

func newToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
