package sqlite

import "testing"

func TestNativeVectorBuildGate(t *testing.T) {
	for _, tc := range []struct {
		version, details string
		want             bool
	}{
		{"v0.1.9", "Version: v0.1.9\nDate: pinned\nCommit: \nBuild flags:  ", true},
		{"v0.1.10", "Version: v0.1.10\nBuild flags: ", false},
		{"v0.1.9", "Version: v0.1.9\nBuild flags: neon", false},
		{"v0.1.9", "Version: v0.1.9\nBuild flags: avx ", false},
		{"v0.1.9", "Version: v0.1.8\nBuild flags: ", false},
		{"v0.1.9", "Version: v0.1.9", false},
	} {
		if got := verifiedVectorExtension(tc.version, tc.details); got != tc.want {
			t.Errorf("version=%q details=%q: got %t want %t", tc.version, tc.details, got, tc.want)
		}
	}
}
