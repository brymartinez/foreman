package knowledge

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Refs name a learning inside one merge, so a local L1 and an imported L1 stay apart.
const (
	LocalRef  = "local:"
	ImportRef = "import:"
)

// Group is one learning of a merge's result: the refs of the learnings it combines,
// with its kind and text. Conflict is the merge agent's reason the members disagree,
// or empty.
type Group struct {
	Members  []string
	Kind     Kind
	Text     string
	Conflict string
}

// MergeItem is one learning a merge must place in a group.
type MergeItem struct {
	Ref      string
	Learning Learning
	// Corrections are the notes of its maintainer evidence, newest last.
	Corrections []string
}

// Merge is a prepared ingest of an export into a repository that already has
// knowledge. Prepare has matched the two sides' tasks; the agent only groups Items.
type Merge struct {
	local, incoming Snapshot
	// Items are every local learning, then every imported one that adds something.
	Items []MergeItem
	// changed holds the refs of learnings that lost a task's evidence to the other
	// side's evaluation of it, so their status is derived again.
	changed map[string]bool
	// imported holds the task ids of stored evaluations that came from the export,
	// whose proposals and revisions name the export's learning ids.
	imported  map[string]bool
	unchanged bool
}

// Restore is x stored as repo's knowledge when repo has none: the same records,
// with a next id no stored learning already uses.
func Restore(x Snapshot, repo string) Snapshot {
	x.Repo = repo
	for _, l := range x.Learnings {
		x.NextID = max(x.NextID, idNumber(l.ID))
	}
	x.NextID = max(x.NextID, len(x.Learnings))
	return x
}

// taskKey is what makes two task records the same task on two machines: the task
// id, with any suffix an earlier ingest added removed, and when the task started.
type taskKey struct {
	base string
	at   time.Time
}

var renamedTask = regexp.MustCompile(`^(.+)\.i([0-9a-f]{8})$`)

// renamed is the id an imported task gets when its id is taken by a different
// local task. It depends only on the task, so a repeat ingest picks the same id.
func renamed(k taskKey) string {
	sum := sha256.Sum256([]byte(k.base + "\n" + k.at.UTC().Format(time.RFC3339Nano)))
	return k.base + ".i" + hex.EncodeToString(sum[:])[:8]
}

// keyOf undoes renamed only when the suffix is the one renamed would give, so an id
// that merely looks renamed keeps its meaning.
func keyOf(id string, at time.Time) taskKey {
	if m := renamedTask.FindStringSubmatch(id); m != nil && renamed(taskKey{m[1], at}) == id {
		return taskKey{m[1], at}
	}
	return taskKey{id, at}
}

// taskKeys maps each task id in x to its key. An evaluation gives the start time;
// evidence without an evaluation carries it too.
func taskKeys(x Snapshot) map[string]taskKey {
	keys := map[string]taskKey{}
	for _, ev := range x.Evaluations {
		keys[ev.Task] = keyOf(ev.Task, ev.TaskAt)
	}
	for _, l := range x.Learnings {
		for _, e := range l.Evidence {
			if _, ok := keys[e.Task]; !ok {
				keys[e.Task] = keyOf(e.Task, e.At)
			}
		}
	}
	for _, l := range x.Learnings {
		for _, c := range l.History {
			if _, ok := keys[c.Task]; !ok {
				keys[c.Task] = keyOf(c.Task, time.Time{})
			}
		}
	}
	return keys
}

// Prepare matches incoming's tasks to local's. The same task on both sides keeps one
// evaluation: the newer one, or the local one when both judged the same evidence.
// The other side's evidence and history from that task are dropped, so no task backs
// a learning twice. A different task under a taken id gets a renamed id. An imported
// learning whose evidence and history are all stored already adds nothing.
func Prepare(local, incoming Snapshot) (*Merge, error) {
	local, incoming = clone(local), clone(incoming)
	m := &Merge{changed: map[string]bool{}, imported: map[string]bool{}}
	localKeys, incomingKeys := taskKeys(local), taskKeys(incoming)
	byKey := map[taskKey]string{}
	taken := map[string]bool{}
	for id, k := range localKeys {
		byKey[k] = id
		taken[id] = true
	}
	localEvals := map[string]int{}
	for i, ev := range local.Evaluations {
		localEvals[ev.Task] = i
	}
	incomingEvals := map[string]int{}
	for i, ev := range incoming.Evaluations {
		incomingEvals[ev.Task] = i
	}

	rename := map[string]string{}
	dropIncoming := map[string]bool{}
	var adopted []Evaluation
	swapped := 0
	for _, id := range sortedKeys(incomingKeys) {
		k := incomingKeys[id]
		lid, same := byKey[k]
		if !same {
			to := id
			if taken[to] {
				to = renamed(k)
			}
			if taken[to] {
				return nil, fmt.Errorf("knowledge: imported task %q collides with local task %q", id, to)
			}
			rename[id], taken[to] = to, true
			continue
		}
		rename[id] = lid
		li, hasLocal := localEvals[lid]
		ii, hasIncoming := incomingEvals[id]
		switch {
		case !hasLocal && !hasIncoming:
			// Neither side has an evaluation, so each keeps its evidence and the
			// union below drops the copies.
		case hasIncoming && (!hasLocal || newer(incoming.Evaluations[ii], local.Evaluations[li])):
			for i := range local.Learnings {
				if dropTask(&local.Learnings[i], lid) {
					m.changed[LocalRef+local.Learnings[i].ID] = true
				}
			}
			ev := incoming.Evaluations[ii]
			ev.Task = lid
			if hasLocal {
				local.Evaluations[li] = ev
				swapped++
			} else {
				adopted = append(adopted, ev)
			}
			m.imported[lid] = true
			dropIncoming[id] = true
		default:
			for i := range incoming.Learnings {
				if dropTask(&incoming.Learnings[i], id) {
					m.changed[ImportRef+incoming.Learnings[i].ID] = true
				}
			}
			dropIncoming[id] = true
		}
	}

	var added []Evaluation
	for _, ev := range incoming.Evaluations {
		if dropIncoming[ev.Task] {
			continue
		}
		ev.Task = rename[ev.Task]
		m.imported[ev.Task] = true
		added = append(added, ev)
	}
	for i := range incoming.Learnings {
		l := &incoming.Learnings[i]
		for j := range l.Evidence {
			l.Evidence[j].Task = rename[l.Evidence[j].Task]
		}
		for j := range l.History {
			l.History[j].Task = rename[l.History[j].Task]
		}
	}
	for i := range incoming.Evaluations {
		if to, ok := rename[incoming.Evaluations[i].Task]; ok {
			incoming.Evaluations[i].Task = to
		}
	}

	evidence, history := map[string]bool{}, map[string]bool{}
	for _, l := range local.Learnings {
		m.Items = append(m.Items, item(LocalRef, l))
		for _, e := range l.Evidence {
			evidence[evidenceKey(e)] = true
		}
		for _, c := range l.History {
			history[changeKey(c)] = true
		}
	}
	news := 0
	for _, l := range incoming.Learnings {
		if !slices.ContainsFunc(l.Evidence, func(e Evidence) bool { return !evidence[evidenceKey(e)] }) &&
			!slices.ContainsFunc(l.History, func(c Change) bool { return !history[changeKey(c)] }) {
			continue
		}
		m.Items = append(m.Items, item(ImportRef, l))
		news++
	}
	local.Evaluations = append(append(local.Evaluations, adopted...), added...)
	sortEvaluations(local.Evaluations)
	m.local, m.incoming = local, incoming
	m.unchanged = news == 0 && len(added) == 0 && len(adopted) == 0 && swapped == 0
	return m, nil
}

// newer reports whether the imported evaluation replaces the local one: it judged
// different evidence, and later.
func newer(in, local Evaluation) bool {
	return in.Fingerprint != local.Fingerprint && in.At.After(local.At)
}

func item(prefix string, l Learning) MergeItem {
	it := MergeItem{Ref: prefix + l.ID, Learning: l}
	for _, e := range l.Evidence {
		if e.Source == Maintainer && e.Note != "" {
			it.Corrections = append(it.Corrections, e.Note)
		}
	}
	return it
}

// dropTask removes task's evidence and history from l and reports whether any was
// there.
func dropTask(l *Learning, task string) bool {
	n, h := len(l.Evidence), len(l.History)
	l.Evidence = slices.DeleteFunc(l.Evidence, func(e Evidence) bool { return e.Task == task })
	l.History = slices.DeleteFunc(l.History, func(c Change) bool { return c.Task == task })
	return len(l.Evidence) != n || len(l.History) != h
}

// Unchanged reports whether the export adds nothing the repository does not hold.
func (m *Merge) Unchanged() bool { return m.unchanged }

// NeedsGrouping reports whether both sides have learnings for the agent to group.
// Otherwise every learning is its own group.
func (m *Merge) NeedsGrouping() bool {
	return slices.ContainsFunc(m.Items, func(it MergeItem) bool { return strings.HasPrefix(it.Ref, LocalRef) }) &&
		slices.ContainsFunc(m.Items, func(it MergeItem) bool { return strings.HasPrefix(it.Ref, ImportRef) })
}

// Refs are the refs of every item, in order.
func (m *Merge) Refs() []string {
	refs := make([]string, len(m.Items))
	for i, it := range m.Items {
		refs[i] = it.Ref
	}
	return refs
}

// Singletons puts each item in a group of its own.
func (m *Merge) Singletons() []Group {
	var gs []Group
	for _, it := range m.Items {
		gs = append(gs, Group{Members: []string{it.Ref}, Kind: it.Learning.Kind, Text: it.Learning.Text})
	}
	return gs
}

// ConflictError means maintainer corrections disagree and their order is not
// reliable. Nothing is stored.
type ConflictError struct {
	Conflicts []Conflict
}

type Conflict struct {
	Reason string
	Sides  []ConflictSide
}

// ConflictSide is one member's guidance and its latest maintainer correction.
type ConflictSide struct {
	Ref       string
	Text      string
	Status    Status
	Corrected time.Time
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "knowledge: %d unresolved conflict(s) between maintainer corrections:", len(e.Conflicts))
	for i, c := range e.Conflicts {
		fmt.Fprintf(&b, "\n%d. %s", i+1, c.Reason)
		for _, s := range c.Sides {
			at := "no time"
			if !s.Corrected.IsZero() {
				at = s.Corrected.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(&b, "\n   - %s (%s, corrected %s): %s", s.Ref, s.Status, at, s.Text)
		}
	}
	return b.String()
}

// reliableOrder is how far apart two maintainer corrections must be for the later
// one to win. Closer than this, clock skew between machines could reverse them.
const reliableOrder = time.Minute

// Apply builds the merged knowledge from groups, which must place every item in
// exactly one group. Go, not the agent, combines evidence and history and sets each
// status from them. Maintainer corrections outrank observations; between
// maintainer corrections that disagree the later wins when reliableOrder separates
// them, and otherwise Apply returns a *ConflictError.
func (m *Merge) Apply(groups []Group) (Snapshot, error) {
	items := map[string]MergeItem{}
	for _, it := range m.Items {
		items[it.Ref] = it
	}
	placed := map[string]bool{}
	for i, g := range groups {
		if len(g.Members) == 0 {
			return Snapshot{}, fmt.Errorf("knowledge: merge group %d has no members", i+1)
		}
		if !validKind(g.Kind) {
			return Snapshot{}, fmt.Errorf("knowledge: merge group %d has unknown kind %q", i+1, g.Kind)
		}
		for _, ref := range g.Members {
			if _, ok := items[ref]; !ok {
				return Snapshot{}, fmt.Errorf("knowledge: merge group %d names unknown learning %q", i+1, ref)
			}
			if placed[ref] {
				return Snapshot{}, fmt.Errorf("knowledge: learning %q is in two merge groups", ref)
			}
			placed[ref] = true
		}
	}
	for _, it := range m.Items {
		if !placed[it.Ref] {
			return Snapshot{}, fmt.Errorf("knowledge: the merge left out learning %q", it.Ref)
		}
	}

	// A group with a local member takes the first one's id and place; the others
	// follow in the agent's order with new ids.
	groups = slices.Clone(groups)
	slices.SortStableFunc(groups, func(a, b Group) int { return cmp.Compare(m.firstLocal(a), m.firstLocal(b)) })
	out := m.local
	out.Learnings = nil
	f := learningsFile{NextID: m.local.NextID}
	for _, l := range m.local.Learnings {
		f.NextID = max(f.NextID, idNumber(l.ID))
	}
	ids := map[string]string{}
	var conflicts []Conflict
	for _, g := range groups {
		members := make([]MergeItem, len(g.Members))
		for i, ref := range g.Members {
			members[i] = items[ref]
		}
		l, conflict, err := m.build(g, members)
		if err != nil {
			return Snapshot{}, err
		}
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
			continue
		}
		if local := m.firstLocal(g); local < len(m.local.Learnings) {
			l.ID = m.local.Learnings[local].ID
		} else {
			l.ID = nextID(&f)
		}
		for _, ref := range g.Members {
			ids[ref] = l.ID
		}
		out.Learnings = append(out.Learnings, l)
	}
	if len(conflicts) > 0 {
		return Snapshot{}, &ConflictError{Conflicts: conflicts}
	}
	out.NextID = max(f.NextID, len(out.Learnings))

	out.Evaluations = slices.Clone(out.Evaluations)
	for i := range out.Evaluations {
		ev := &out.Evaluations[i]
		prefix := LocalRef
		if m.imported[ev.Task] {
			prefix = ImportRef
		}
		ev.Proposals = slices.Clone(ev.Proposals)
		for j := range ev.Proposals {
			var supports []string
			for _, id := range ev.Proposals[j].Supports {
				if to, ok := ids[prefix+id]; ok && !slices.Contains(supports, to) {
					supports = append(supports, to)
				}
			}
			ev.Proposals[j].Supports = supports
		}
		ev.Revisions = slices.Clone(ev.Revisions)
		for j := range ev.Revisions {
			// A revision of a learning the merge dropped keeps no id, so it cannot
			// point at an unrelated learning that happens to share it.
			ev.Revisions[j].ID = ids[prefix+ev.Revisions[j].ID]
		}
	}
	return out, nil
}

// firstLocal is the position of g's first local member among the local learnings,
// or past the end when it has none.
func (m *Merge) firstLocal(g Group) int {
	pos := len(m.local.Learnings)
	for _, ref := range g.Members {
		for i, l := range m.local.Learnings {
			if ref == LocalRef+l.ID && i < pos {
				pos = i
			}
		}
	}
	return pos
}

// stance is what one member's latest maintainer correction says: retire it, or
// keep its text. Without a correction, retired is the member's status.
type stance struct {
	item     MergeItem
	retired  bool
	latest   time.Time
	task     string
	explicit bool
	// text is the learning's text when the latest correction was recorded, so a
	// later observed rewrite does not stand in for what the maintainer backed.
	text string
}

// agrees reports whether two stances say the same. Their texts count only in a
// group the agent flagged; otherwise it judged them the same guidance.
func (s stance) agrees(o stance, byText bool) bool {
	return s.retired == o.retired && (!byText || normalize(s.text) == normalize(o.text))
}

// build combines one group's members into a learning, or reports their conflict.
func (m *Merge) build(g Group, members []MergeItem) (Learning, *Conflict, error) {
	if len(members) == 1 {
		l := members[0].Learning
		if m.changed[members[0].Ref] {
			l.Status = replay(l)
		}
		return l, nil, nil
	}
	text, err := Sanitize(g.Text)
	if err != nil {
		return Learning{}, nil, fmt.Errorf("knowledge: merged text for %s: %v", strings.Join(g.Members, ", "), err)
	}
	l := Learning{Kind: g.Kind, Text: text}
	evidence, history := map[string]bool{}, map[string]bool{}
	var stances []stance
	for _, it := range members {
		for _, e := range it.Learning.Evidence {
			if k := evidenceKey(e); !evidence[k] {
				evidence[k] = true
				l.Evidence = append(l.Evidence, e)
			}
		}
		for _, c := range it.Learning.History {
			if k := changeKey(c); !history[k] {
				history[k] = true
				l.History = append(l.History, c)
			}
		}
		stances = append(stances, m.stanceOf(it))
	}
	slices.SortStableFunc(l.History, func(a, b Change) int { return a.At.Compare(b.At) })

	disagree := slices.ContainsFunc(members, func(it MergeItem) bool {
		return (it.Learning.Status == Retired) != (members[0].Learning.Status == Retired)
	})
	var explicit []stance
	for _, s := range stances {
		if s.explicit {
			explicit = append(explicit, s)
		}
	}
	// Observed work alone follows the promotion rules. Any maintainer correction
	// decides the status, even when every member is retired by observation.
	if len(explicit) == 0 {
		l.Status = replay(l)
		return l, nil, nil
	}
	slices.SortStableFunc(explicit, func(a, b stance) int { return b.latest.Compare(a.latest) })
	winner := explicit[0]
	var rivals []stance
	for _, s := range explicit[1:] {
		if !s.agrees(winner, g.Conflict != "") {
			rivals = append(rivals, s)
		}
	}
	if len(rivals) > 0 && (winner.latest.IsZero() || rivals[0].latest.IsZero() || winner.latest.Sub(rivals[0].latest) < reliableOrder) {
		c := Conflict{Reason: cmp.Or(g.Conflict, "maintainer corrections disagree on whether to keep this learning")}
		for _, s := range append([]stance{winner}, rivals...) {
			c.Sides = append(c.Sides, ConflictSide{Ref: s.item.Ref, Text: s.text, Status: s.item.Learning.Status, Corrected: s.latest})
		}
		return Learning{}, &c, nil
	}
	// The entries that record the decision sort after every entry they override, so
	// a later merge, which orders history by time, replays the same result.
	decided := winner.latest
	for _, c := range l.History {
		decided = later(decided, c.At)
	}
	decided = decided.Add(time.Second)
	if g.Conflict != "" || disagree || len(rivals) > 0 {
		// The rewrite names the winning correction's task, so a later merge reads
		// the text that correction backed from it, not from other members' rewrites.
		if replayed := textAt(l, decided); winner.text != l.Text || replayed != winner.text {
			l.History = append(l.History, Change{At: decided, Task: winner.task, Action: Revise, From: replayed, To: winner.text, Reason: ingestText})
		}
		l.Text, l.Kind = winner.text, winner.item.Learning.Kind
	}
	switch {
	case winner.retired && !retiredByHistory(l.History):
		l.History = append(l.History, Change{At: decided, Task: winner.task, Action: Retire, Reason: ingestRetired})
	case !winner.retired && retiredByHistory(l.History):
		l.History = append(l.History, Change{At: decided, Task: winner.task, Action: "restore", Reason: ingestRestored})
	}
	l.Status = replay(l)
	return l, nil, nil
}

// The reasons on the history entries a merge adds. They record a decision, not a
// correction, so stanceOf skips them; the correction behind it stays in the
// evidence or history with its own time.
const (
	ingestRetired  = "ingest: the latest maintainer correction retires it"
	ingestRestored = "ingest: the latest maintainer correction keeps it"
	ingestText     = "ingest: the latest maintainer correction's text"
)

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// stanceOf finds the member's latest maintainer correction: maintainer evidence or
// a restore or confirm keeps it, and a retirement its task's evaluation attributes
// to a maintainer retires it. A correction is timed by the evaluation that recorded
// it, not by when its task started; one without an evaluation has no time.
func (m *Merge) stanceOf(it MergeItem) stance {
	s := stance{item: it, retired: it.Learning.Status == Retired}
	side := m.local
	if strings.HasPrefix(it.Ref, ImportRef) {
		side = m.incoming
	}
	mark := func(at time.Time, task string, retire bool) {
		if !s.explicit || at.After(s.latest) {
			s.latest, s.task, s.retired = at, task, retire
		}
		s.explicit = true
	}
	for _, e := range it.Learning.Evidence {
		if e.Source == Maintainer {
			mark(evaluatedAt(side, e.Task), e.Task, false)
		}
	}
	history := it.Learning.History
	for i, c := range history {
		if c.Reason == ingestRetired || c.Reason == ingestRestored || c.Reason == ingestText {
			continue
		}
		// A re-evaluation of the task withdrew the entry, so the maintainer no
		// longer stands behind it.
		switch c.Action {
		case "restore", "confirm":
			if !withdrawn(history[i+1:], "revert-restore", c.Task) {
				mark(c.At, c.Task, false)
			}
		case Retire:
			if !withdrawn(history[i+1:], "revert-retire", c.Task) && maintainerRetire(side, c.Task, it.Learning.ID) {
				mark(c.At, c.Task, true)
			}
		}
	}
	s.text = it.Learning.Text
	if s.explicit {
		s.text = textAt(it.Learning, s.latest)
		// An earlier merge recorded the text of the correction it chose. It sorts
		// after that correction, so a correction by the same task recorded after it
		// supersedes it, and textAt already gives that correction's text.
		for _, c := range live(history, Revise, "revert-rewrite") {
			if c.Reason == ingestText && c.Task == s.task && !c.At.Before(s.latest) {
				s.text = c.To
			}
		}
	}
	return s
}

// textAt is l's text as of at: the newest rewrite still in force recorded by
// then, or the original text.
func textAt(l Learning, at time.Time) string {
	text := originalText(l)
	for _, c := range live(l.History, Revise, "revert-rewrite") {
		if !c.At.After(at) {
			text = c.To
		}
	}
	return text
}

// evaluatedAt is when task's evaluation in x was recorded, or zero.
func evaluatedAt(x Snapshot, task string) time.Time {
	for _, ev := range x.Evaluations {
		if ev.Task == task {
			return ev.At
		}
	}
	return time.Time{}
}

func maintainerRetire(x Snapshot, task, id string) bool {
	for _, ev := range x.Evaluations {
		if ev.Task != task {
			continue
		}
		return slices.ContainsFunc(ev.Revisions, func(r Revision) bool {
			return r.ID == id && r.Action == Retire && r.Source == Maintainer
		})
	}
	return false
}

// replay is the status l's history and evidence give it, by apply's rules.
func replay(l Learning) Status {
	if retiredByHistory(l.History) {
		return Retired
	}
	return derive(l)
}

func evidenceKey(e Evidence) string {
	return strings.Join([]string{e.Task, e.At.UTC().Format(time.RFC3339Nano), string(e.Source), e.Note, strings.Join(e.Cites, ",")}, "\x00")
}

func changeKey(c Change) string {
	return strings.Join([]string{c.At.UTC().Format(time.RFC3339Nano), c.Task, c.Action, c.From, c.To, c.Reason}, "\x00")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func clone(x Snapshot) Snapshot {
	x.Learnings = slices.Clone(x.Learnings)
	for i := range x.Learnings {
		x.Learnings[i].Evidence = slices.Clone(x.Learnings[i].Evidence)
		x.Learnings[i].History = slices.Clone(x.Learnings[i].History)
	}
	x.Evaluations = slices.Clone(x.Evaluations)
	return x
}
