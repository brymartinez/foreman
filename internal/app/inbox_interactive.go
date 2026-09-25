package app

import (
	"bufio"
	"fmt"
	"io"
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

// ChooseInbox prints the current refresh and collects one delegation choice.
// It returns nil when the user quits or reaches EOF.
func ChooseInbox(in io.Reader, out io.Writer, report InboxReport) (*InboxSelection, error) {
	if len(report.Current) == 0 {
		fmt.Fprintln(out, "Nothing to choose.")
		return nil, nil
	}

	fmt.Fprintln(out, "INBOX")
	for i, entry := range report.Current {
		key := entry.Item.Key
		fmt.Fprintf(out, "  %d. %s#%d  %s · %s\n", i+1, key.Repo, key.Number, entry.Item.Title, strings.Join(entry.Changes, ", "))
	}
	fmt.Fprintln(out, "  q. Quit")

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "\nChoose an item: ")
		line, ok := scanLine(scanner)
		if !ok || strings.EqualFold(line, "q") {
			return nil, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(report.Current) {
			fmt.Fprintf(out, "Please choose a number from 1 to %d, or q to quit.\n", len(report.Current))
			continue
		}

		agent, ok := chooseAgent(scanner, out)
		if !ok {
			return nil, nil
		}
		fmt.Fprint(out, "Context file (optional): ")
		contextFile, ok := scanLine(scanner)
		if !ok {
			return nil, nil
		}
		return &InboxSelection{Key: report.Current[n-1].Item.Key, Agent: agent, ContextFile: contextFile}, nil
	}
}

func chooseAgent(scanner *bufio.Scanner, out io.Writer) (task.Agent, bool) {
	for {
		fmt.Fprint(out, "Agent (claude/codex): ")
		line, ok := scanLine(scanner)
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

func scanLine(scanner *bufio.Scanner) (string, bool) {
	if !scanner.Scan() {
		return "", false
	}
	return strings.TrimSpace(scanner.Text()), true
}
