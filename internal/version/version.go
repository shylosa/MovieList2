// Package version provides the release version embedded into the application.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var source string

// Current is release metadata, independent of runtime configuration.
var Current = strings.TrimSpace(source)
