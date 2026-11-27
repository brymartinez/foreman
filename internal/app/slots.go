package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/brymartinez/foreman/internal/task"
)

// ErrNoSlot refuses an automatic start while auto.max_concurrent runs are under way.
var ErrNoSlot = errors.New("waiting for a free slot")

// startsInFlight holds the tasks this process is starting or about to start. A start
// takes a slot only when its first update commits, so a count from the database
// alone can pass the limit while another start is still resolving.
var (
	startsMu       sync.Mutex
	startsInFlight = map[task.Key]int{}
)

// ReserveSlot holds one of the auto.max_concurrent slots for an automatic start of
// key until release is called. A queued start and the dashboard's auto mode both
// reserve before they resolve a route and a branch, so they never take the same one.
func (a App) ReserveSlot(ctx context.Context, key task.Key) (release func(), err error) {
	startsMu.Lock()
	defer startsMu.Unlock()
	if a.MaxConcurrent > 0 {
		recs, err := a.Store.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("count the active runs: %w", err)
		}
		if n := slotsInUse(recs); n >= a.MaxConcurrent {
			return nil, fmt.Errorf("%w: %d runs are active and auto.max_concurrent is %d", ErrNoSlot, n, a.MaxConcurrent)
		}
	}
	return holdSlot(slotKey(key)), nil
}

// trackStart counts a start under way, whether or not it reserved a slot first.
func trackStart(key task.Key, repo task.Repo, issue int) func() {
	if key == (task.Key{}) {
		key = task.Key{Repo: repo, Number: issue}
	}
	startsMu.Lock()
	defer startsMu.Unlock()
	return holdSlot(slotKey(key))
}

// holdSlot needs startsMu held. The release it returns takes the lock itself.
func holdSlot(slot task.Key) func() {
	startsInFlight[slot]++
	return sync.OnceFunc(func() {
		startsMu.Lock()
		defer startsMu.Unlock()
		if startsInFlight[slot]--; startsInFlight[slot] <= 0 {
			delete(startsInFlight, slot)
		}
	})
}

// slotsInUse counts the active runs and the starts the task list does not show
// active yet. It needs startsMu held.
func slotsInUse(recs []*task.Record) int {
	active := map[task.Key]bool{}
	n := 0
	for _, r := range recs {
		if r.State.Active() {
			active[slotKey(r.Key)] = true
			n++
		}
	}
	for k := range startsInFlight {
		if !active[k] {
			n++
		}
	}
	return n
}

// slotKey identifies a task for counting. A local issue is one task whether or not
// its repository is chosen yet.
func slotKey(key task.Key) task.Key {
	switch key.Source {
	case task.Local:
		return task.Key{Source: task.Local, ExternalID: key.ExternalID}
	case task.GitHub:
		key.Source = ""
	}
	return key
}
