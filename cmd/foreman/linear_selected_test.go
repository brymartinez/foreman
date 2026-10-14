package main

import (
	"testing"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/task"
)

func TestLinearCredentialsNeededOnlyForVisibleLinearProjects(t *testing.T) {
	cfg := config.Config{LinearProjects: map[task.Repo]string{"acme/web": "project"}}
	if !linearSelected(cfg, "") || !linearSelected(cfg, task.Linear) || linearSelected(cfg, task.GitHub) {
		t.Fatal("source selection did not match the mapped repository")
	}
	cfg.IncludedRepos = []task.Repo{"acme/api"}
	if linearSelected(cfg, "") {
		t.Fatal("excluded Linear project required credentials")
	}
	cfg.IncludedRepos = nil
	cfg.ExcludedRepos = []task.Repo{"acme/web"}
	if linearSelected(cfg, "") {
		t.Fatal("excluded Linear project required credentials")
	}
}
