package scripts

import _ "embed"

// InstallScript is the canonical shell installer embedded for self-updates.
//
//go:embed install.sh
var InstallScript []byte
