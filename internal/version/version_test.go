package version

import (
	"regexp"
	"strings"
	"testing"
)

func TestReleaseVersionIsEmbeddedAndIndependentOfEnvironment(t *testing.T) {
	t.Setenv("APP_VERSION", "runtime-override-must-be-ignored")
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Current) {
		t.Fatalf("invalid release version: %q", Current)
	}
	if Current != strings.TrimSpace(source) {
		t.Fatal("environment changed release metadata")
	}
}
