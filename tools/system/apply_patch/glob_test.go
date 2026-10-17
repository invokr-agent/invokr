package apply_patch

import "testing"

func TestMatchesGlobSemantics(t *testing.T) {
	cases := []struct {
		path string
		glob string
		want bool
	}{
		{".env", ".env", true},
		{"sub/.env", ".env", false},
		{"server.pem", "*.pem", true},
		{"id_rsa", "**/id_*", true},
		{"home/user/id_rsa", "**/id_*", true},
		{"a/b/.ssh/authorized_keys", "**/.ssh/**", true},
		{".ssh/config", "**/.ssh/**", true},
		{"known_hosts", "**/known_hosts", true},
		{"deep/nested/known_hosts", "**/known_hosts", true},
		// No substring fallback: partial name overlaps must not match.
		{"docs/known_hosts_guide.md", "**/known_hosts", false},
		{"src/hide_rsa_docs.go", "**/id_*", false},
		{"notes/env.md", ".env", false},
		// Invalid patterns simply do not match.
		{"anything.go", "[", false},
		{"any[thing.go", "**/[", false},
		{"file.go", "", false},
	}
	for _, tc := range cases {
		if got := matchesAnyGlob(tc.path, []string{tc.glob}); got != tc.want {
			t.Fatalf("matchesAnyGlob(%q, %q) = %v, want %v", tc.path, tc.glob, got, tc.want)
		}
	}
}
