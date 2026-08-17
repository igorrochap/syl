package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type notificationBackend string

const (
	notificationMacOS        notificationBackend = "macos"
	notificationNotifySend   notificationBackend = "notify-send"
	notificationPowerShell   notificationBackend = "powershell.exe"
	notificationTerminalBell notificationBackend = "terminal-bell"
)

// selectNotificationBackend keeps platform detection separate from command
// execution so the selection rules can be tested without desktop processes.
func selectNotificationBackend(goos, kernelRelease string, environment map[string]string) notificationBackend {
	if goos == "darwin" {
		return notificationMacOS
	}
	if strings.Contains(strings.ToLower(kernelRelease), "microsoft") || strings.Contains(strings.ToLower(kernelRelease), "wsl2") {
		return notificationPowerShell
	}
	if goos == "linux" && (environment["DISPLAY"] != "" || environment["WAYLAND_DISPLAY"] != "" || environment["XDG_CURRENT_DESKTOP"] != "") {
		return notificationNotifySend
	}
	return notificationTerminalBell
}

type platformNotifier struct {
	backend notificationBackend
	stderr  *os.File
}

func newPlatformNotifier() Notifier {
	environment := map[string]string{
		"DISPLAY":             os.Getenv("DISPLAY"),
		"WAYLAND_DISPLAY":     os.Getenv("WAYLAND_DISPLAY"),
		"XDG_CURRENT_DESKTOP": os.Getenv("XDG_CURRENT_DESKTOP"),
	}
	kernelRelease := ""
	if output, err := exec.Command("uname", "-r").Output(); err == nil {
		kernelRelease = string(output)
	}
	return &platformNotifier{
		backend: selectNotificationBackend(runtime.GOOS, kernelRelease, environment),
		stderr:  os.Stderr,
	}
}

func (n *platformNotifier) Notify(ctx context.Context, message string) error {
	if n == nil {
		return nil
	}
	switch n.backend {
	case notificationMacOS:
		if _, err := exec.LookPath("terminal-notifier"); err == nil {
			if err := runNotificationCommand(ctx, "terminal-notifier", "-title", "rig", "-message", message); err == nil {
				return nil
			}
			return n.ringFallback()
		}
		if err := runNotificationCommand(ctx, "osascript", "-e", fmt.Sprintf(`display notification %q with title "rig"`, message)); err == nil {
			return nil
		}
		return n.ringFallback()
	case notificationNotifySend:
		if err := runNotificationCommand(ctx, "notify-send", "rig", message); err == nil {
			return nil
		}
		return n.ringFallback()
	case notificationPowerShell:
		command := fmt.Sprintf(`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null; $xml = New-Object Windows.Data.Xml.Dom.XmlDocument; $xml.LoadXml('<toast><visual><binding template="ToastGeneric"><text>rig</text><text>%s</text></binding></visual></toast>'); [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('rig').Show([Windows.UI.Notifications.ToastNotification]::new($xml))`, strings.ReplaceAll(message, "'", "&apos;"))
		if err := runNotificationCommand(ctx, "powershell.exe", "-NoProfile", "-Command", command); err == nil {
			return nil
		}
		return n.ringFallback()
	default:
		return n.ringFallback()
	}
}

func (n *platformNotifier) ringFallback() error {
	if n.stderr == nil {
		return nil
	}
	_, err := n.stderr.WriteString("\a")
	return err
}

func runNotificationCommand(ctx context.Context, name string, args ...string) error {
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		return fmt.Errorf("send notification with %s: %w", name, err)
	}
	return nil
}
