// Package updater checks for and installs newer syl releases.
package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/scripts"
)

const defaultReleaseURL = "https://api.github.com/repos/igorrochap/syl/releases/latest"

// Result describes the release check and whether an installation occurred.
type Result struct {
	CurrentVersion string
	LatestVersion  string
	Updated        bool
}

// Runner is the update boundary used by the CLI.
type Runner interface {
	Update(context.Context, string) (Result, error)
}

// HTTPClient is the HTTP boundary used to query the GitHub release API.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

var _ HTTPClient = (*http.Client)(nil)

// Installer installs a release archive at the supplied executable path.
type Installer interface {
	Install(context.Context, string, string) error
}

// Options configures an Updater. Zero-valued options use the production
// GitHub endpoint, HTTP client, executable lookup, and shell installer.
type Options struct {
	Client     HTTPClient
	Installer  Installer
	Executable func() (string, error)
	ReleaseURL string
}

// Updater checks GitHub for a newer release and installs it when available.
type Updater struct {
	client     HTTPClient
	installer  Installer
	executable func() (string, error)
	releaseURL string
}

var _ Runner = (*Updater)(nil)

// New constructs an Updater with production defaults for omitted options.
func New(options Options) *Updater {
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	if options.Installer == nil {
		options.Installer = NewShellInstaller()
	}
	if options.Executable == nil {
		options.Executable = os.Executable
	}
	if options.ReleaseURL == "" {
		options.ReleaseURL = defaultReleaseURL
	}
	return &Updater{
		client:     options.Client,
		installer:  options.Installer,
		executable: options.Executable,
		releaseURL: options.ReleaseURL,
	}
}

// Default constructs an Updater for the production environment.
func Default() *Updater {
	return New(Options{})
}

// Update checks the latest release and installs it when it is newer than the
// supplied version. It does not resolve the executable path unless an update
// is needed, so an up-to-date check cannot touch the running binary.
func (u *Updater) Update(ctx context.Context, currentVersion string) (Result, error) {
	current, err := parseVersion(currentVersion)
	if err != nil {
		return Result{}, err
	}

	latestVersion, err := u.latestRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	latest, err := parseVersion(latestVersion)
	if err != nil {
		return Result{}, fmt.Errorf("latest release has invalid version %q: %w", latestVersion, err)
	}

	result := Result{CurrentVersion: currentVersion, LatestVersion: latestVersion}
	if compareVersions(latest, current) <= 0 {
		return result, nil
	}

	executable, err := u.executable()
	if err != nil {
		return Result{}, fmt.Errorf("find running binary: %w", err)
	}
	if executable == "" {
		return Result{}, fmt.Errorf("find running binary: executable path is empty")
	}
	if err := u.installer.Install(ctx, executable, latestVersion); err != nil {
		return Result{}, fmt.Errorf("install %s: %w", latestVersion, err)
	}

	result.Updated = true
	return result, nil
}

func (u *Updater) latestRelease(ctx context.Context) (string, error) {
	response, err := u.fetchLatestRelease(ctx)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	return decodeLatestRelease(response)
}

func (u *Updater) fetchLatestRelease(ctx context.Context) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.releaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "syl")

	response, err := u.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("check latest release: %w", err)
	}
	if response == nil {
		return nil, fmt.Errorf("check latest release: response is nil")
	}
	if response.Body == nil {
		return nil, fmt.Errorf("check latest release: response has no body")
	}
	return response, nil
}

func decodeLatestRelease(response *http.Response) (string, error) {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		if readErr != nil {
			return "", fmt.Errorf("check latest release: GitHub returned %s: read response: %w", response.Status, readErr)
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			return "", fmt.Errorf("check latest release: GitHub returned %s", response.Status)
		}
		return "", fmt.Errorf("check latest release: GitHub returned %s: %s", response.Status, message)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	tagName := strings.TrimSpace(release.TagName)
	if tagName == "" {
		return "", fmt.Errorf("decode latest release: tag_name is empty")
	}
	return tagName, nil
}

// ShellInstaller delegates installation to the canonical shell installer.
// Keeping the script embedded means the update command and curl-based
// installation use exactly the same release and checksum logic.
type ShellInstaller struct {
	Script []byte
}

var _ Installer = (*ShellInstaller)(nil)

// NewShellInstaller returns an installer using scripts/install.sh.
func NewShellInstaller() ShellInstaller {
	return ShellInstaller{Script: scripts.InstallScript}
}

// Install runs the canonical installer against one exact executable path.
func (i ShellInstaller) Install(ctx context.Context, executable, release string) error {
	if len(i.Script) == 0 {
		return fmt.Errorf("installer script is empty")
	}
	stagedExecutable, err := os.CreateTemp(filepath.Dir(executable), ".syl-update.*")
	if err != nil {
		return fmt.Errorf("create staged executable: %w", err)
	}
	stagedPath := stagedExecutable.Name()
	if err := stagedExecutable.Close(); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("close staged executable: %w", err)
	}
	defer os.Remove(stagedPath)

	tempDir, err := os.MkdirTemp("", "syl-update.")
	if err != nil {
		return fmt.Errorf("create installer temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	scriptPath := filepath.Join(tempDir, "install.sh")
	if err := os.WriteFile(scriptPath, i.Script, 0o700); err != nil {
		return fmt.Errorf("write installer script: %w", err)
	}

	command := exec.CommandContext(ctx, "sh", scriptPath, "--path", stagedPath, "--version", release)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			return fmt.Errorf("run installer: %w", err)
		}
		return fmt.Errorf("run installer: %w: %s", err, detail)
	}
	if err := os.Rename(stagedPath, executable); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease []string
}

func parseVersion(raw string) (semanticVersion, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "dev" || trimmed == "" {
		return semanticVersion{}, fmt.Errorf("cannot update development version %q", raw)
	}
	if strings.HasPrefix(trimmed, "v") {
		trimmed = trimmed[1:]
	}
	if trimmed == "" {
		return semanticVersion{}, fmt.Errorf("invalid semantic version %q", raw)
	}

	if buildIndex := strings.IndexByte(trimmed, '+'); buildIndex >= 0 {
		trimmed = trimmed[:buildIndex]
	}
	core := trimmed
	prerelease := ""
	if prereleaseIndex := strings.IndexByte(trimmed, '-'); prereleaseIndex >= 0 {
		core = trimmed[:prereleaseIndex]
		prerelease = trimmed[prereleaseIndex+1:]
		if prerelease == "" {
			return semanticVersion{}, fmt.Errorf("invalid semantic version %q", raw)
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("invalid semantic version %q", raw)
	}

	values := [3]uint64{}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, fmt.Errorf("invalid semantic version %q", raw)
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("invalid semantic version %q: %w", raw, err)
		}
		values[index] = value
	}

	version := semanticVersion{major: values[0], minor: values[1], patch: values[2]}
	if prerelease != "" {
		for _, identifier := range strings.Split(prerelease, ".") {
			if identifier == "" || (isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0') {
				return semanticVersion{}, fmt.Errorf("invalid semantic version %q", raw)
			}
			version.prerelease = append(version.prerelease, identifier)
		}
	}
	return version, nil
}

func compareVersions(left, right semanticVersion) int {
	for _, values := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if values[0] < values[1] {
			return -1
		}
		if values[0] > values[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		leftIdentifier, rightIdentifier := left.prerelease[index], right.prerelease[index]
		leftNumeric, rightNumeric := isNumeric(leftIdentifier), isNumeric(rightIdentifier)
		switch {
		case leftNumeric && rightNumeric:
			leftValue, _ := strconv.ParseUint(leftIdentifier, 10, 64)
			rightValue, _ := strconv.ParseUint(rightIdentifier, 10, 64)
			if leftValue < rightValue {
				return -1
			}
			if leftValue > rightValue {
				return 1
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case leftIdentifier < rightIdentifier:
			return -1
		case leftIdentifier > rightIdentifier:
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}

func isNumeric(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}
