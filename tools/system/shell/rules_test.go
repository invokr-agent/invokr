package shell

import "testing"

func TestClassifyByRuleTiers(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		want    shellTier
	}{
		{name: "unknown command fails closed", command: "perl", args: []string{"-e", "1"}, want: shellTierWrite},
		{name: "unknown subcommand fails closed", command: "npm", args: []string{"frobnicate"}, want: shellTierWrite},
		{name: "nil allowlist stays readonly", command: "ls", args: []string{"-laR"}, want: shellTierReadonly},
		{name: "cat readonly", command: "cat", args: []string{"-n", "main.go"}, want: shellTierReadonly},
		{name: "grep readonly", command: "grep", args: []string{"-rn", "TODO", "."}, want: shellTierReadonly},
		{name: "find read flags readonly", command: "find", args: []string{".", "-name", "*.go"}, want: shellTierReadonly},
		{name: "find delete writes", command: "find", args: []string{".", "-delete"}, want: shellTierWrite},
		{name: "find exec writes", command: "find", args: []string{".", "-exec", "rm", "{}", ";"}, want: shellTierWrite},
		{name: "find fprintf writes", command: "find", args: []string{".", "-fprintf", "/tmp/x", "%p"}, want: shellTierWrite},
		{name: "find ok writes", command: "find", args: []string{".", "-ok", "rm", "{}", ";"}, want: shellTierWrite},
		{name: "cat absolute path writes", command: "cat", args: []string{"/etc/passwd"}, want: shellTierWrite},
		{name: "cat parent escape writes", command: "cat", args: []string{"../secret"}, want: shellTierWrite},
		{name: "grep absolute path writes", command: "grep", args: []string{"-r", "x", "/home/user/.ssh"}, want: shellTierWrite},
		{name: "find absolute path writes", command: "find", args: []string{"/etc", "-name", "*.conf"}, want: shellTierWrite},
		{name: "rg read flags readonly", command: "rg", args: []string{"-n", "-g", "*.go", "TODO", "."}, want: shellTierReadonly},
		{name: "rg pre writes", command: "rg", args: []string{"--pre", "sh", "TODO", "."}, want: shellTierWrite},
		{name: "rg pre value form writes", command: "rg", args: []string{"--pre=sh", "TODO", "."}, want: shellTierWrite},
		{name: "flag value escaping workspace writes", command: "wc", args: []string{"--files0-from=/etc/passwd"}, want: shellTierWrite},
		{name: "gofmt list readonly", command: "gofmt", args: []string{"-l", "."}, want: shellTierReadonly},
		{name: "gofmt write flag writes", command: "gofmt", args: []string{"-w", "."}, want: shellTierWrite},
		{name: "gofmt rewrite flag writes", command: "gofmt", args: []string{"-r", "a->b"}, want: shellTierWrite},
		{name: "go env readonly", command: "go", args: []string{"env"}, want: shellTierReadonly},
		{name: "go env write flag writes", command: "go", args: []string{"env", "-w", "GOFLAGS=-mod=mod"}, want: shellTierWrite},
		{name: "go env unset flag writes", command: "go", args: []string{"env", "-u", "GOFLAGS"}, want: shellTierWrite},
		{name: "npm ls readonly", command: "npm", args: []string{"ls"}, want: shellTierReadonly},
		{name: "npm install network", command: "npm", args: []string{"install"}, want: shellTierNetwork},
		{name: "cargo check readonly", command: "cargo", args: []string{"check"}, want: shellTierReadonly},
		{name: "cargo fetch network", command: "cargo", args: []string{"fetch"}, want: shellTierNetwork},
		// #545: without a row these fell to the write tier, where
		// allow_write_commands ran them unprompted and without network.
		{name: "curl network", command: "curl", args: []string{"https://example.com"}, want: shellTierNetwork},
		{name: "curl output flag still network", command: "curl", args: []string{"-o", "/tmp/x", "https://example.com"}, want: shellTierNetwork},
		{name: "wget network", command: "wget", args: []string{"https://example.com"}, want: shellTierNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyShellCommand(test.command, test.args); got != test.want {
				t.Fatalf("classifyShellCommand(%q, %v) = %q, want %q", test.command, test.args, got, test.want)
			}
		})
	}
}

// TestCommandRulesNeverClassifyCodeExecutorsReadonly guards the table against a
// future edit smuggling a project-code executor into the no-approval tier.
func TestCommandRulesNeverClassifyCodeExecutorsReadonly(t *testing.T) {
	executors := []struct {
		command string
		args    []string
	}{
		{command: "node", args: []string{"index.js"}},
		{command: "python3", args: []string{"-c", "print(1)"}},
		{command: "pytest", args: []string{"--collect-only"}},
		{command: "make", args: []string{"-n"}},
		{command: "npm", args: []string{"run", "build"}},
		{command: "cargo", args: []string{"run"}},
		{command: "go", args: []string{"test", "./..."}},
		{command: "go", args: []string{"run", "main.go"}},
		{command: "rg", args: []string{"--pre", "sh", "pattern"}},
	}
	for _, executor := range executors {
		if got := classifyShellCommand(executor.command, executor.args); got == shellTierReadonly {
			t.Fatalf("classifyShellCommand(%q, %v) = readonly, want a tier that asks", executor.command, executor.args)
		}
	}
}

func TestCommandRulesRowsAreExactlyOneShape(t *testing.T) {
	for command, rule := range commandRules {
		hasSubcommands := len(rule.Readonly) > 0 || len(rule.Network) > 0
		if hasSubcommands && rule.ReadFlags != nil {
			t.Fatalf("rule %q sets both a subcommand set and ReadFlags", command)
		}
		if !hasSubcommands && len(rule.WriteFlags) > 0 && !rule.AlwaysNetwork {
			t.Fatalf("rule %q is flag-only but sets WriteFlags", command)
		}
		if rule.AlwaysNetwork && (hasSubcommands || rule.ReadFlags != nil) {
			t.Fatalf("rule %q is AlwaysNetwork but also sets a subcommand set or ReadFlags", command)
		}
	}
}
