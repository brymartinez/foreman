package app

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/brymartinez/foreman/internal/task"
)

// InboxSelection is the user's choice from the current inbox refresh. Resume means
// the task already has a run waiting on PR feedback, so it continues instead of
// starting.
type InboxSelection struct {
	Key         task.Key
	Agent       task.Agent
	ContextFile string
	Resume      bool
}

// Delegable lists the items in the current refresh that the developer can hand over.
// Pull requests are left out, because only an issue can be delegated, and so are closed
// items. An item foreman has never delegated is offered unless only a tracked re-fetch
// found it. One it has delegated is offered only when the run has stopped and is the
// developer's to move: FAILED, which starts again, and REVIEWED, which resumes. Work an
// agent still holds stays in DELEGATED WORK.
func Delegable(report InboxReport) []InboxEntry {
	var choices []InboxEntry
	for _, entry := range report.Current {
		if CanDelegate(entry) {
			choices = append(choices, entry)
		}
	}
	return choices
}

// CanDelegate reports one entry against those rules. It is exported so the renderer
// can report activity for exactly the items the picker leaves out.
func CanDelegate(entry InboxEntry) bool {
	if entry.Item.Kind == task.PullRequest || entry.Item.State == "closed" || entry.Item.State == "merged" {
		return false
	}
	if entry.Delegated {
		return entry.State == task.Failed || entry.State == task.Reviewed
	}
	return !slices.Contains(entry.Item.Reasons, task.Tracked)
}

// ChooseInbox prints the delegable issues in the current refresh and collects one
// delegation choice. It returns nil when the user quits or reaches EOF. in is shared
// with any earlier prompt, so no input is buffered away from the next reader. A
// non-nil reload lets v switch between the configured repositories and all of them;
// the choice lasts until ChooseInbox returns.
func ChooseInbox(in *bufio.Reader, out io.Writer, report InboxReport, reload func(allRepos bool) (InboxReport, error)) (*InboxSelection, error) {
	choices := Delegable(report)
	if len(choices) == 0 && reload == nil {
		fmt.Fprintln(out, "No issues to delegate.")
		return nil, nil
	}
	allRepos := false
	printChoices(out, choices, reload != nil, allRepos)

	for {
		fmt.Fprint(out, "\nChoose an item: ")
		line, ok := readLine(in)
		if !ok || strings.EqualFold(line, "q") {
			return nil, nil
		}
		if reload != nil && strings.EqualFold(line, "v") {
			allRepos = !allRepos
			report, err := reload(allRepos)
			if err != nil {
				return nil, err
			}
			choices = Delegable(report)
			fmt.Fprintln(out)
			printChoices(out, choices, true, allRepos)
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(choices) {
			switch {
			case reload == nil:
				fmt.Fprintf(out, "Please choose a number from 1 to %d, or q to quit.\n", len(choices))
			case len(choices) == 0:
				fmt.Fprintln(out, "Please choose v to change the view, or q to quit.")
			default:
				fmt.Fprintf(out, "Please choose a number from 1 to %d, v to change the view, or q to quit.\n", len(choices))
			}
			continue
		}

		// A task with feedback waiting keeps its agent, worktree, and session, so there
		// is nothing left to ask.
		if choice := choices[n-1]; choice.State == task.Reviewed {
			return &InboxSelection{Key: choice.Item.Key, Resume: true}, nil
		}

		agent, ok := chooseAgent(in, out)
		if !ok {
			return nil, nil
		}
		fmt.Fprint(out, "Context file (optional): ")
		contextFile, ok := readLine(in)
		if !ok {
			return nil, nil
		}
		return &InboxSelection{Key: choices[n-1].Item.Key, Agent: agent, ContextFile: contextFile}, nil
	}
}

// printChoices lists choices. toggle names the active view and offers v.
func printChoices(out io.Writer, choices []InboxEntry, toggle, allRepos bool) {
	switch {
	case !toggle:
		fmt.Fprintln(out, "INBOX")
	case allRepos:
		fmt.Fprintln(out, "INBOX (all repositories)")
	default:
		fmt.Fprintln(out, "INBOX (configured repositories)")
	}
	if len(choices) == 0 {
		fmt.Fprintln(out, "  No issues to delegate.")
	}
	for i, entry := range choices {
		key := entry.Item.Key
		detail := strings.Join(entry.Changes, ", ")
		if entry.Delegated {
			detail = string(entry.State) + " · " + detail
		}
		fmt.Fprintf(out, "  %d. %s#%d  %s · %s\n", i+1, key.Repo, key.Number, entry.Item.Title, detail)
	}
	if toggle && allRepos {
		fmt.Fprintln(out, "  v. View configured repositories")
	} else if toggle {
		fmt.Fprintln(out, "  v. View all repositories")
	}
	fmt.Fprintln(out, "  q. Quit")
}

func chooseAgent(in *bufio.Reader, out io.Writer) (task.Agent, bool) {
	for {
		fmt.Fprint(out, "Agent (claude/codex): ")
		line, ok := readLine(in)
		if !ok || strings.EqualFold(line, "q") {
			return "", false
		}
		switch strings.ToLower(line) {
		case string(task.Claude):
			return task.Claude, true
		case string(task.Codex):
			return task.Codex, true
		default:
			fmt.Fprintln(out, "Please choose claude, codex, or q to quit.")
		}
	}
}

func readLine(in *bufio.Reader) (string, bool) {
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}
