package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdateOutcomes(t *testing.T) {
	tests := []struct {
		name           string
		currentVersion string
		latestVersion  string
		executable     string
		want           Result
		wantInstall    bool
	}{
		{
			name:           "installs newer release",
			currentVersion: "v1.10.0",
			latestVersion:  "v1.11.0",
			executable:     "/usr/local/bin/syl",
			want: Result{
				CurrentVersion: "v1.10.0",
				LatestVersion:  "v1.11.0",
				Updated:        true,
			},
			wantInstall: true,
		},
		{
			name:           "does not install current release",
			currentVersion: "1.10.0",
			latestVersion:  "v1.10.0",
			want: Result{
				CurrentVersion: "1.10.0",
				LatestVersion:  "v1.10.0",
			},
		},
		{
			name:           "uses semantic version ordering",
			currentVersion: "v1.9.99",
			latestVersion:  "v1.10.0",
			executable:     "/tmp/syl",
			want: Result{
				CurrentVersion: "v1.9.99",
				LatestVersion:  "v1.10.0",
				Updated:        true,
			},
			wantInstall: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newLatestReleaseClient(t, fmt.Sprintf(`{"tag_name":%q}`, test.latestVersion))
			installer := &recordingInstaller{}
			updater := New(Options{
				Client:    server,
				Installer: installer,
				Executable: func() (string, error) {
					if !test.wantInstall {
						return "", fmt.Errorf("executable path must not be requested")
					}
					return test.executable, nil
				},
				ReleaseURL: "https://example.test/latest",
			})

			result, err := updater.Update(context.Background(), test.currentVersion)
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if result != test.want {
				t.Fatalf("Update() result = %#v, want %#v", result, test.want)
			}
			if installer.called != test.wantInstall {
				t.Fatalf("installer called = %t, want %t", installer.called, test.wantInstall)
			}
			if test.wantInstall && (installer.executable != test.executable || installer.release != test.latestVersion) {
				t.Fatalf(
					"installer call = (%q, %q), want (%q, %q)",
					installer.executable,
					installer.release,
					test.executable,
					test.latestVersion,
				)
			}
		})
	}
}

func TestUpdateRejectsDevelopmentVersion(t *testing.T) {
	server := newLatestReleaseClient(t, `{"tag_name":"v1.11.0"}`)

	updater := New(Options{Client: server, ReleaseURL: "https://example.test/latest"})
	_, err := updater.Update(context.Background(), "dev")
	if err == nil || !strings.Contains(err.Error(), "development version") {
		t.Fatalf("Update() error = %v, want development version error", err)
	}
}

func TestShellInstallerLeavesExistingBinaryOnChecksumFailure(t *testing.T) {
	testRoot := t.TempDir()
	releaseDir := filepath.Join(testRoot, "releases", "download", "v1.11.0")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}

	asset := "syl_" + releaseOS() + "_" + releaseArch() + ".tar.gz"
	archivePath := filepath.Join(releaseDir, asset)
	if err := os.WriteFile(archivePath, []byte("not a valid archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte("000000  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	executable := filepath.Join(testRoot, "syl")
	original := []byte("old binary")
	if err := os.WriteFile(executable, original, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYL_DOWNLOAD_BASE", "file://"+filepath.Join(testRoot, "releases"))

	installer := NewShellInstaller()
	err := installer.Install(context.Background(), executable, "v1.11.0")
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("Install() error = %v, want checksum verification failure", err)
	}
	got, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("existing binary changed to %q after checksum failure", got)
	}
}

func TestShellInstallerLeavesExistingBinaryWhenScriptFailsAfterStaging(t *testing.T) {
	testRoot := t.TempDir()
	executable := filepath.Join(testRoot, "syl")
	original := []byte("old binary")
	if err := os.WriteFile(executable, original, 0o755); err != nil {
		t.Fatal(err)
	}

	installer := ShellInstaller{Script: []byte("#!/bin/sh\nprintf 'new binary' > \"$2\"\nexit 1\n")}
	err := installer.Install(context.Background(), executable, "v1.11.0")
	if err == nil || !strings.Contains(err.Error(), "run installer") {
		t.Fatalf("Install() error = %v, want installer failure", err)
	}
	got, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("existing binary changed to %q after staged installation failure", got)
	}
}

func TestShellInstallerInstallsVerifiedArchiveAtExactPath(t *testing.T) {
	testRoot := t.TempDir()
	releaseDir := filepath.Join(testRoot, "releases", "download", "v1.11.0")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}

	asset := "syl_" + releaseOS() + "_" + releaseArch() + ".tar.gz"
	archivePath := filepath.Join(releaseDir, asset)
	newBinary := []byte("new binary")
	if err := writeArchive(archivePath, newBinary); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%x  %s\n", checksum, asset)
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}

	executable := filepath.Join(testRoot, "renamed-syl")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYL_DOWNLOAD_BASE", "file://"+filepath.Join(testRoot, "releases"))

	if err := NewShellInstaller().Install(context.Background(), executable, "v1.11.0"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBinary) {
		t.Fatalf("installed binary = %q, want %q", got, newBinary)
	}
}

func newLatestReleaseClient(t *testing.T, response string) *latestReleaseClient {
	t.Helper()
	return &latestReleaseClient{t: t, response: response}
}

type latestReleaseClient struct {
	t        *testing.T
	response string
}

func (c *latestReleaseClient) Do(request *http.Request) (*http.Response, error) {
	if request.URL.Path != "/latest" {
		c.t.Errorf("request path = %q, want /latest", request.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(c.response)),
		Header:     make(http.Header),
	}, nil
}

type recordingInstaller struct {
	called     bool
	executable string
	release    string
}

func releaseOS() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "darwin":
		return "Darwin"
	default:
		return runtime.GOOS
	}
}

func releaseArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func (i *recordingInstaller) Install(_ context.Context, executable, release string) error {
	i.called = true
	i.executable = executable
	i.release = release
	return nil
}

func writeArchive(path string, contents []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	archive := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(archive)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "syl", Mode: 0o755, Size: int64(len(contents))}); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := tarWriter.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := tarWriter.Close(); err != nil {
		_ = file.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
