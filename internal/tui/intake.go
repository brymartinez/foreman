package tui

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Intake owns the saved conversation and the transaction that confirms a local issue.
type Intake interface {
	Load(context.Context) (IntakeView, error)
	Reply(context.Context, int64, string) (IntakeView, error)
	Save(context.Context, int64) (string, error)
	Discard(context.Context, int64) error
}

type IntakeView struct {
	Revision int64
	Messages []IntakeMessage
	Pending  string
	Draft    *IntakeDraft
}

type IntakeMessage struct {
	User bool
	Text string
}

type IntakeDraft struct {
	Title, Description, SourceURL, Repo, Decisions string
}

type intakePanel uint8

const (
	intakeClosed intakePanel = iota
	intakeChat
	intakeReview
	intakeDiscard
)

type intakeState struct {
	panel     intakePanel
	inFlight  ActionKind
	view      IntakeView
	input     string
	err       string
	chatBack  int
	reviewTop int
	loaded    bool
}

const maxIntakeInput = 64 << 10

func (m *Model) openIntake() Action {
	s := &m.intake
	s.panel, s.chatBack = intakeChat, 0
	m.LogPaused = false
	if s.inFlight != ActionNone {
		return Action{}
	}
	s.loaded, s.err = false, ""
	return s.request(ActionLoadIntake)
}

func (s *intakeState) request(kind ActionKind) Action {
	s.inFlight = kind
	s.err = ""
	return Action{Kind: kind, IntakeRevision: s.view.Revision, Answer: s.input}
}

func (m *Model) intakeKey(k Key) Action {
	s := &m.intake
	if k.Kind == KeyEscape {
		if s.panel == intakeChat {
			s.panel = intakeClosed
		} else {
			s.panel = intakeChat
		}
		return Action{}
	}
	if s.inFlight != ActionNone {
		return Action{}
	}
	page := m.intakePage()

	switch s.panel {
	case intakeReview:
		switch k.Kind {
		case KeyUp:
			s.reviewTop = max(s.reviewTop-1, 0)
		case KeyDown:
			s.reviewTop = min(s.reviewTop+1, max(len(m.intakeLines())-page, 0))
		case KeyPageUp:
			s.reviewTop = max(s.reviewTop-page, 0)
		case KeyPageDown:
			s.reviewTop = min(s.reviewTop+page, max(len(m.intakeLines())-page, 0))
		case KeyHome:
			s.reviewTop = 0
		case KeyEnd:
			s.reviewTop = max(len(m.intakeLines())-page, 0)
		case KeyRune:
			if k.Rune == 'y' {
				return s.request(ActionSaveIntake)
			}
		}
	case intakeDiscard:
		if k.Kind == KeyRune && k.Rune == 'y' {
			return s.request(ActionDiscardIntake)
		}
	case intakeChat:
		switch k.Kind {
		case KeyEnter:
			if !s.loaded {
				return s.request(ActionLoadIntake)
			}
			if strings.TrimSpace(s.input) != "" {
				s.chatBack = 0
				return s.request(ActionReplyIntake)
			}
		case KeyTab:
			if s.loaded && s.view.Draft != nil && strings.TrimSpace(s.input) == "" {
				s.panel, s.reviewTop, s.err = intakeReview, 0, ""
			}
		case KeyCtrlD:
			if s.loaded && (len(s.view.Messages) > 0 || s.view.Pending != "" || s.input != "" || s.view.Draft != nil) {
				s.panel, s.err = intakeDiscard, ""
			}
		case KeyBackspace:
			_, n := utf8.DecodeLastRuneInString(s.input)
			s.input = s.input[:len(s.input)-n]
			s.err = ""
		case KeyRune:
			s.appendInput(string(k.Rune))
		case KeyPaste:
			s.appendInput(k.Text)
		case keyPasteTooLarge:
			s.err = "The pasted text is too long. Shorten it and paste again."
		case KeyUp:
			s.chatBack = min(s.chatBack+1, max(len(m.intakeLines())-page, 0))
		case KeyDown:
			s.chatBack = max(s.chatBack-1, 0)
		case KeyPageUp:
			s.chatBack = min(s.chatBack+page, max(len(m.intakeLines())-page, 0))
		case KeyPageDown:
			s.chatBack = max(s.chatBack-page, 0)
		case KeyHome:
			s.chatBack = max(len(m.intakeLines())-page, 0)
		case KeyEnd:
			s.chatBack = 0
		}
	}
	return Action{}
}

func (s *intakeState) appendInput(text string) {
	if len(s.input)+len(text) > maxIntakeInput {
		s.err = "The reply is too long. Shorten it before sending."
		return
	}
	s.input += text
	s.err = ""
}

func runIntake(ctx context.Context, service Intake, a Action) result {
	r := result{intake: a.Kind}
	switch a.Kind {
	case ActionLoadIntake:
		r.intakeView, r.err = service.Load(ctx)
	case ActionReplyIntake:
		r.intakeView, r.err = service.Reply(ctx, a.IntakeRevision, a.Answer)
	case ActionSaveIntake:
		r.intakeID, r.err = service.Save(ctx, a.IntakeRevision)
	case ActionDiscardIntake:
		r.err = service.Discard(ctx, a.IntakeRevision)
		if r.err == nil {
			r.intakeDiscarded = true
			r.intakeView, r.err = service.Load(ctx)
		}
	}
	return r
}

func (m *Model) applyIntake(r result) {
	s := &m.intake
	s.inFlight = ActionNone
	if r.intakeDiscarded {
		panel := s.panel
		if panel != intakeClosed {
			panel = intakeChat
		}
		*s = intakeState{panel: panel}
	}
	if r.err != nil {
		s.err = r.err.Error()
		if r.intake == ActionReplyIntake && r.intakeView.Revision > s.view.Revision {
			s.view = r.intakeView
			if s.view.Pending != "" {
				s.input = s.view.Pending
			}
		}
		if s.panel == intakeClosed {
			m.Message = "! intake: " + s.err
		}
		return
	}

	switch r.intake {
	case ActionLoadIntake, ActionDiscardIntake:
		// Preserve unsent typing when the same thread is reopened in this dashboard.
		if r.intakeView.Revision != s.view.Revision || s.input == "" {
			s.input = r.intakeView.Pending
		}
		s.loaded, s.view = true, r.intakeView
	case ActionReplyIntake:
		s.loaded, s.view, s.input = true, r.intakeView, r.intakeView.Pending
	case ActionSaveIntake:
		*s = intakeState{}
		m.Message = "Saved local issue " + r.intakeID
	}
}

func (m Model) intakePage() int { return max(m.Height-4, 1) }

func (m Model) intakeLines() []string {
	var lines []string
	add := func(label, text string) {
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			lines = append(lines, wrapLine(printable(label+line), m.Width)...)
			label = ""
		}
		lines = append(lines, "")
	}
	s := m.intake
	switch s.panel {
	case intakeReview:
		if d := s.view.Draft; d != nil {
			add("Title: ", d.Title)
			if d.SourceURL != "" {
				add("Source: ", d.SourceURL)
			}
			if d.Repo != "" {
				add("Repository: ", d.Repo)
			}
			add("Description", "\n"+d.Description)
			add("Triage decisions", "\n"+d.Decisions)
		}
	case intakeDiscard:
		add("", "Discard this conversation and its draft? You can start again here.")
	case intakeChat:
		add("Foreman: ", "Paste a link to the issue or paste the issue text.")
		for _, msg := range s.view.Messages {
			label := "Agent: "
			if msg.User {
				label = "You: "
			}
			add(label, msg.Text)
		}
		if s.inFlight == ActionReplyIntake {
			add("You: ", s.input)
		}
	}
	return lines
}

func intakeFrame(m Model) []string {
	s := m.intake
	title := "LOCAL ISSUE · TRIAGE"
	status := s.err
	field := ""
	hints := "esc back"
	switch s.inFlight {
	case ActionLoadIntake:
		status = "Loading the conversation…"
	case ActionReplyIntake:
		status = "Waiting for the agent…"
	case ActionSaveIntake:
		status = "Saving the issue…"
	case ActionDiscardIntake:
		status = "Discarding the conversation…"
	case ActionNone:
		switch s.panel {
		case intakeChat:
			field = "> " + lastColumns(printable(s.input), m.Width-2)
			hints = "enter send · ↑↓ scroll · esc back"
			if !s.loaded {
				hints = "enter retry loading · esc back"
				field = ""
			} else if s.view.Draft != nil && strings.TrimSpace(s.input) == "" {
				hints = "enter send · tab review · esc back"
			}
			if s.loaded && (len(s.view.Messages) > 0 || s.view.Pending != "" || s.input != "" || s.view.Draft != nil) {
				hints += " · ctrl-d discard"
			}
		case intakeReview:
			hints = "y save issue · ↑↓ scroll · esc back"
		case intakeDiscard:
			hints = "y discard · esc back"
		}
	}

	switch s.panel {
	case intakeReview:
		title = "LOCAL ISSUE · REVIEW DRAFT"
	case intakeDiscard:
		title = "LOCAL ISSUE · DISCARD CONVERSATION"
	}
	row := func(s string) string { return pad(truncate(s, m.Width), m.Width) }
	lines := []string{colored(row(title), cyan)}
	body := m.intakeLines()
	page := m.intakePage()
	top := max(len(body)-page-s.chatBack, 0)
	if s.panel != intakeChat {
		top = min(s.reviewTop, max(len(body)-page, 0))
	}
	for _, line := range body[top:min(top+page, len(body))] {
		lines = append(lines, row(line))
	}
	for len(lines) < m.Height-3 {
		lines = append(lines, row(""))
	}
	return append(lines, row(field), row(status), dimmed(row(hints)))
}
