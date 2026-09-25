package notifications

import (
	"fmt"
	"os/exec"
	runtime "runtime"
)

type Native struct{}

func (Native) Notify(title, body string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title %q", body, title)).Run()
	case "linux":
		return exec.Command("notify-send", title, body).Run()
	default:
		return nil
	}
}
