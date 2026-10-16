package app

import (
	"crypto/rand"
	"encoding/hex"
	"os"

	"github.com/brymartinez/foreman/internal/task"
)

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
