package cli

import (
	"strings"
)

func contextIndicator(context string) string {
	if strings.TrimSpace(context) == "" {
		return ""
	}
	return " · context"
}
