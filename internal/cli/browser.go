package cli

import (
	"fmt"
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	command, arguments, err := browserCommand(url)
	if err != nil {
		return err
	}
	if err := exec.Command(command, arguments...).Start(); err != nil {
		return fmt.Errorf("run %s: %w", command, err)
	}
	return nil
}

func browserCommand(url string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}, nil
	case "linux":
		return "xdg-open", []string{url}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	default:
		return "", nil, fmt.Errorf("browser opening is unsupported on %s", runtime.GOOS)
	}
}
