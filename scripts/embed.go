package scripts

import _ "embed"

// InstallScript is the canonical shell installer embedded for self-updates.
//
//go:embed install.sh
var InstallScript []byte

// QualityScript is the language-neutral quality gate skeleton initialized in a project.
//
//go:embed quality.sh
var QualityScript []byte
