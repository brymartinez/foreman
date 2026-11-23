package workspace_test

import (
	"testing"

	"github.com/brymartinez/foreman/internal/workspace"
	"github.com/brymartinez/foreman/internal/workspace/backendtest"
)

func TestMemoryBackendMeetsTheContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Env {
		m := &workspace.Memory{}
		return backendtest.Env{
			Backend:    m,
			UserCreate: m.UserCreate,
			UserEdit:   func(ref string, fn func(*workspace.TaskEntry)) { m.UserEdit(ref, fn) },
		}
	})
}
