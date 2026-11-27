package cli

import "testing"

func TestStorageVerbsParse(t *testing.T) {
	for argv, want := range map[string]Name{"check": StorageCheck, "move": StorageMove} {
		cmd, err := Parse([]string{"storage", argv})
		if err != nil || cmd.Name != want {
			t.Errorf("storage %s = %+v, %v", argv, cmd, err)
		}
	}
}

func TestStorageRejectsAMissingOrUnknownVerbAndExtraArguments(t *testing.T) {
	for _, argv := range [][]string{{"storage"}, {"storage", "copy"}, {"storage", "check", "extra"}, {"storage", "move", "--force"}} {
		if _, err := Parse(argv); err == nil {
			t.Errorf("%v parsed", argv)
		} else if _, ok := err.(UsageError); !ok {
			t.Errorf("%v: error %T is not a usage error", argv, err)
		}
	}
}

func TestUsageNamesTheStorageVerbs(t *testing.T) {
	for _, want := range []string{"foreman storage check", "foreman storage move"} {
		if !containsLine(Usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}

func containsLine(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
