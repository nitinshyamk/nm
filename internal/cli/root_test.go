package cli

import "testing"

func TestBuildVersionPrefersTheLdflagsOverride(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = "v1.2.3"
	if got := buildVersion(); got != "v1.2.3" {
		t.Errorf("buildVersion() = %q, want the -ldflags value", got)
	}
}

// Without the override nm must still say something real. The old code reported
// a hardcoded "dev" for every build that did not go through the mise task,
// which included `go install` and any plain `go build`.
func TestBuildVersionFallsBackToBuildInfo(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = ""
	got := buildVersion()
	if got == "" {
		t.Fatal("buildVersion() is empty, so `nm --version` would print nothing")
	}
	// A test binary is not stamped with VCS information, so "dev" is the honest
	// answer here; what matters is that the fallback chain terminates.
	t.Logf("buildVersion() without an override = %q", got)
}

func TestRootCommandReportsAVersion(t *testing.T) {
	if got := newRootCmd().Version; got == "" {
		t.Error("the root command has no Version, so `nm --version` is unavailable")
	}
}
