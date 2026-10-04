package cmd

import "testing"

func TestBuildVersion(t *testing.T) {
	for _, tc := range []struct{ set, recorded, want string }{
		{devVersion, "v1.2.3", "v1.2.3"},           // go install
		{devVersion, "(devel)", devVersion},        // go test
		{devVersion, "", devVersion},               // no build info
		{"v1.2.4", "v0.0.0-2026-abcdef", "v1.2.4"}, // release build
	} {
		if got := buildVersion(tc.set, tc.recorded); got != tc.want {
			t.Errorf("buildVersion(%q, %q) = %q, want %q", tc.set, tc.recorded, got, tc.want)
		}
	}
}
