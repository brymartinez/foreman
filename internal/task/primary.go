package task

import "time"

// LocalPrimary returns the record that stands for the local issue id, or nil. A
// local issue holds one record per repository it had work in. A delegation binds
// the issue, then a reset mark does. Imported records that never started bind
// nothing, and the newest import wins among them.
func LocalPrimary(recs []*Record, id string) *Record {
	var best *Record
	for _, r := range recs {
		if r.Source != Local || r.ExternalID != id {
			continue
		}
		if best == nil || r.Binding() > best.Binding() || r.Binding() == best.Binding() && r.boundAt().After(best.boundAt()) {
			best = r
		}
	}
	return best
}

// Binding ranks how firmly a record ties its issue to its repository. Zero is a
// record that never started, which binds nothing.
func (r *Record) Binding() int {
	switch {
	case r.Delegation != nil:
		return 2
	case r.Reset != nil:
		return 1
	}
	return 0
}

func (r *Record) boundAt() time.Time {
	switch {
	case r.Delegation != nil:
		return r.Delegation.StartedAt
	case r.Reset != nil:
		return r.Reset.At
	case r.Imported != nil:
		return r.Imported.At
	}
	return time.Time{}
}
