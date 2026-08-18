// Package skills exposes the vendored skill set to the syl binary.
package skills

import "embed"

// Assets contains the manifest and every vendored skill file.
//
// The directory is embedded so an installed syl binary can initialize a
// project without needing this repository on disk.
//
//go:embed manifest.json */*
var Assets embed.FS
