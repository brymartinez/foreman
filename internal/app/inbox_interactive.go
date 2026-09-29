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

// InboxSelection is the user's choice from the current inbox refresh.
type InboxSelection struct {
	Key         task.Key
	Agent       task.Agent
	ContextFile string
}

// Delegable lists the items in the current refresh that can be handed to an agent.
// Pull requests are left out, because only an issue can be delegated, and so are
// closed items and ones that only a tracked re-fetch found.
func Delegable(report InboxReport) []InboxEntry {
	var choices []InboxEntry
	for _, entry := range report.Current {
		if entry.Item.Kind != task.PullRequest && !slices.Contains(entry.Item.Reasons, task.Tracked) && entry.Item.State != "closed" && entry.Item.State != "merged" {
			choices = append(choices, entry)
		}
	}
	return choices
}

// ChooseInbox prints the delegable issues in the current refresh and collects one
// delegation choice. It returns nil when the user quits or reaches EOF. in is shared
// with any earlier prompt, so no input is buffered away from the next reader.
func ChooseInbox(in *bufio.Reader, out io.Writer, report InboxReport) (*InboxSelection, error) {
	choices := Delegable(report)
	if len(choices) == 0 {
		fmt.Fprintln(out, "No issues to delegate.")
		return nil, nil
	}

	fmt.Fprintln(out, "INBOX")
	for i, entry := range choices {
		key := entry.Item.Key
		fmt.Fprintf(out, "  %d. %s#%d  %s · %s\n", i+1, key.Repo, key.Number, entry.Item.Title, strings.Join(entry.Changes, ", "))
	}
	fmt.Fprintln(out, "  q. Quit")

	for {
		fmt.Fprint(out, "\nChoose an item: ")
		line, ok := readLine(in)
		if !ok || strings.EqualFold(line, "q") {
			return nil, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(choices) {
			fmt.Fprintf(out, "Please choose a number from 1 to %d, or q to quit.\n", len(choices))
			continue
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
