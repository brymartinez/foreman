package tui

import (
	"fmt"
	"strings"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

func (m *Model) RepositoryProposed(seq int, p app.RepositoryProposal, err error) {
	if m.Mode != ResolvingRepository || seq != m.proposalSeq {
		return
	}
	m.Mode, m.Input = TypingRepository, string(p.Repo)
	m.Message = p.Reason
	if err != nil {
		m.Message = "Could not infer repository: " + err.Error() + "; enter owner/repo."
	}
}

func (m *Model) typeRepository(k Key) Action {
	switch k.Kind {
	case KeyEscape:
		m.Mode, m.Input = Browsing, ""
	case KeyBackspace:
		r := []rune(m.Input)
		if len(r) > 0 {
			m.Input = string(r[:len(r)-1])
		}
	case KeyRune:
		m.Input += string(k.Rune)
	case KeyEnter:
		repo, err := task.ParseRepo(strings.TrimSpace(m.Input))
		if err != nil {
			m.Message = fmt.Sprint(err)
			return Action{}
		}
		m.pending.Repo, m.pending.Key.Repo = repo, repo
		m.Mode, m.Input, m.Message = ChoosingAgent, "", ""
	}
	return Action{}
}
