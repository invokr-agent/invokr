package docker_sandbox_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/docker_sandbox"
)

func TestBuildDockerArgs(t *testing.T) {
	type tc struct {
		name          string
		engine        string
		image         string
		workspaceRoot string
		invokrHome    string
		agentArgs     []string
		isTTY         bool
		selinux       bool
		check         func(t *testing.T, argv []string)
	}

	containsPair := func(argv []string, flag, val string) bool {
		for i := 0; i < len(argv)-1; i++ {
			if argv[i] == flag && argv[i+1] == val {
				return true
			}
		}
		return false
	}

	tests := []tc{
		{
			name: "tty adds -it", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr", isTTY: true,
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if argv[1] != "run" {
					t.Fatalf("argv[1]=%q want run", argv[1])
				}
				if !slices.Contains(argv, "-it") {
					t.Fatalf("expected -it in argv")
				}
				// -it must appear before --rm
				itIdx := slices.Index(argv, "-it")
				rmIdx := slices.Index(argv, "--rm")
				if itIdx > rmIdx {
					t.Fatalf("-it at %d must be before --rm at %d", itIdx, rmIdx)
				}
			},
		},
		{
			name: "no tty omits -it", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if slices.Contains(argv, "-it") {
					t.Fatalf("expected no -it in argv")
				}
			},
		},
		{
			name: "engine propagates", engine: "podman", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if argv[0] != "podman" {
					t.Fatalf("argv[0]=%q want podman", argv[0])
				}
			},
		},
		{
			name: "workspace volume", engine: "docker", image: "img", workspaceRoot: "/srv/proj", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-v", "/srv/proj:/srv/proj") {
					t.Fatalf("expected -v /srv/proj:/srv/proj in argv: %v", argv)
				}
			},
		},
		{
			name: "invokr home volume", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/root/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-v", "/root/.invokr:/root/.invokr") {
					t.Fatalf("expected -v /root/.invokr:/root/.invokr in argv: %v", argv)
				}
			},
		},
		{
			name: "workdir", engine: "docker", image: "img", workspaceRoot: "/srv/proj", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-w", "/srv/proj") {
					t.Fatalf("expected -w /srv/proj in argv: %v", argv)
				}
			},
		},
		{
			name: "INVOKR_IN_SANDBOX forced", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-e", "INVOKR_IN_SANDBOX=1") {
					t.Fatalf("expected -e INVOKR_IN_SANDBOX=1 in argv: %v", argv)
				}
			},
		},
		{
			name: "ALLOW_UNSANDBOXED forced", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-e", "INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1") {
					t.Fatalf("expected -e INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1 in argv: %v", argv)
				}
			},
		},
		{
			name:   "agent args forwarded",
			engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr",
			agentArgs: []string{"agent", "--model", "gpt-4o"},
			check: func(t *testing.T, argv []string) {
				t.Helper()
				n := len(argv)
				if n < 3 || argv[n-3] != "agent" || argv[n-2] != "--model" || argv[n-1] != "gpt-4o" {
					t.Fatalf("expected last 3 args [agent --model gpt-4o], got %v", argv[n-3:])
				}
			},
		},
		{
			name: "path with spaces", engine: "docker", image: "img", workspaceRoot: "/srv/my project", invokrHome: "/home/u/.invokr",
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-v", "/srv/my project:/srv/my project") {
					t.Fatalf("expected -v '/srv/my project:/srv/my project' in argv: %v", argv)
				}
			},
		},
		{
			name: "selinux appends :Z", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr", selinux: true,
			check: func(t *testing.T, argv []string) {
				t.Helper()
				if !containsPair(argv, "-v", "/work:/work:Z") {
					t.Fatalf("expected -v /work:/work:z in argv: %v", argv)
				}
				if !containsPair(argv, "-v", "/home/u/.invokr:/root/.invokr:Z") {
					t.Fatalf("expected -v /home/u/.invokr:/root/.invokr:z in argv: %v", argv)
				}
			},
		},
		{
			name: "no selinux no :Z", engine: "docker", image: "img", workspaceRoot: "/work", invokrHome: "/home/u/.invokr", selinux: false,
			check: func(t *testing.T, argv []string) {
				t.Helper()
				for _, a := range argv {
					if strings.HasSuffix(a, ":z") || strings.HasSuffix(a, ":Z") {
						t.Fatalf("unexpected SELinux label in argv element %q", a)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv := docker_sandbox.BuildDockerArgs(tt.engine, tt.image, tt.workspaceRoot, tt.invokrHome, tt.agentArgs, tt.isTTY, tt.selinux, "")
			tt.check(t, argv)
		})
	}
}

func TestFilterEnv(t *testing.T) {
	hasKey := func(out []string, key string) bool {
		prefix := key + "="
		for i := 0; i < len(out)-1; i++ {
			if out[i] == "-e" && strings.HasPrefix(out[i+1], prefix) {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name    string
		input   []string
		present []string
		absent  []string
	}{
		{name: "invokr vars pass", input: []string{"INVOKR_MODEL=gpt-4o"}, present: []string{"INVOKR_MODEL"}},
		{name: "openai key passes", input: []string{"OPENAI_API_KEY=sk-x"}, present: []string{"OPENAI_API_KEY"}},
		{name: "TERM passes", input: []string{"TERM=xterm-256color"}, present: []string{"TERM"}},
		{name: "unrelated blocked", input: []string{"HOME=/root", "PATH=/usr/bin"}, absent: []string{"HOME", "PATH"}},
		{name: "INVOKR_IN_SANDBOX blocked", input: []string{"INVOKR_IN_SANDBOX=1"}, absent: []string{"INVOKR_IN_SANDBOX"}},
		{name: "INVOKR_TOOLBOX_ALLOW_UNSANDBOXED blocked", input: []string{"INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1"}, absent: []string{"INVOKR_TOOLBOX_ALLOW_UNSANDBOXED"}},
		{
			name:    "mixed env",
			input:   []string{"INVOKR_MODEL=x", "AWS_SECRET_ACCESS_KEY=s", "OPENAI_API_KEY=k", "INVOKR_IN_SANDBOX=1"},
			present: []string{"INVOKR_MODEL", "OPENAI_API_KEY"},
			absent:  []string{"AWS_SECRET_ACCESS_KEY", "INVOKR_IN_SANDBOX"},
		},
		{name: "COLORTERM passes", input: []string{"COLORTERM=truecolor"}, present: []string{"COLORTERM"}},
		{name: "NO_COLOR passes", input: []string{"NO_COLOR=1"}, present: []string{"NO_COLOR"}},
		{name: "empty input", input: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := docker_sandbox.FilterEnv(tt.input)
			for _, k := range tt.present {
				if !hasKey(out, k) {
					t.Fatalf("expected key %q in output %v", k, out)
				}
			}
			for _, k := range tt.absent {
				if hasKey(out, k) {
					t.Fatalf("expected key %q to be absent in output %v", k, out)
				}
			}
		})
	}
}

func TestRawArgRequiresSandbox(t *testing.T) {
	tests := []struct {
		args     []string
		expected bool
	}{
		{[]string{"agent"}, false},
		{[]string{"--sandbox", "agent"}, true},
		{[]string{"--sandbox=true", "agent"}, true},
		{[]string{"--sandbox=1", "agent"}, true},
		{[]string{}, false},
		{[]string{"--no-sandbox"}, false},
	}

	for _, tt := range tests {
		got := docker_sandbox.RawArgRequiresSandbox(tt.args)
		if got != tt.expected {
			t.Fatalf("RawArgRequiresSandbox(%v) = %v, want %v", tt.args, got, tt.expected)
		}
	}
}

func TestRawArgDisablesSandbox(t *testing.T) {
	tests := []struct {
		args     []string
		expected bool
	}{
		{[]string{"agent"}, false},
		{[]string{"--sandbox=false", "agent"}, true},
		{[]string{"--sandbox=0", "agent"}, true},
		{[]string{"--sandbox", "false", "agent"}, true},
		{[]string{"--sandbox", "0", "agent"}, true},
		{[]string{"--sandbox=true", "agent"}, false},
		{[]string{"--sandbox", "agent"}, false},
		{[]string{}, false},
	}

	for _, tt := range tests {
		got := docker_sandbox.RawArgDisablesSandbox(tt.args)
		if got != tt.expected {
			t.Fatalf("RawArgDisablesSandbox(%v) = %v, want %v", tt.args, got, tt.expected)
		}
	}
}

func TestHostLoopbackArgs(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    []string
	}{
		{
			name:    "localhost rewritten to host gateway",
			baseURL: "http://localhost:8080",
			want:    []string{"--add-host", "host.docker.internal:host-gateway", "-e", "INVOKR_BASE_URL=http://host.docker.internal:8080"},
		},
		{
			name:    "127.0.0.1 rewritten",
			baseURL: "http://127.0.0.1:8080/v1",
			want:    []string{"--add-host", "host.docker.internal:host-gateway", "-e", "INVOKR_BASE_URL=http://host.docker.internal:8080/v1"},
		},
		{name: "remote host untouched", baseURL: "https://api.example.com/v1", want: nil},
		{name: "empty untouched", baseURL: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := docker_sandbox.HostLoopbackArgs(tt.baseURL)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("HostLoopbackArgs(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestMemoryDirPinnedToMount(t *testing.T) {
	t.Setenv("INVOKR_HOME", "/home/u/.invokr")
	t.Setenv("INVOKR_MEMORY_DIR", "/home/u/.invokr")

	argv := docker_sandbox.BuildDockerArgs("docker", "img", "/work", "/home/u/.invokr", nil, false, false, "")
	joined := strings.Join(argv, " ")

	for _, want := range []string{
		"-v /home/u/.invokr:/root/.invokr",
		"-e INVOKR_HOME=/root/.invokr",
		"-e INVOKR_MEMORY_DIR=/root/.invokr",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v missing %q", argv, want)
		}
	}
	if strings.Contains(joined, "-e INVOKR_HOME=/home/u/.invokr") ||
		strings.Contains(joined, "-e INVOKR_MEMORY_DIR=/home/u/.invokr") {
		t.Fatalf("host-absolute memory paths leaked into argv: %v", argv)
	}
}
