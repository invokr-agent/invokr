package toolbox

import "testing"

func TestValidatePythonRequirementsAcceptsPinned(t *testing.T) {
	data := []byte("# comment\n\nrequests==2.31.0\nnumpy[extra]==1.26.4  ; python_version >= \"3.8\"\n")
	if _, err := parsePythonRequirementNames(data); err != nil {
		t.Fatalf("expected pinned requirements to pass, got: %v", err)
	}
}

func TestValidatePythonRequirementsRejectsUnpinned(t *testing.T) {
	for _, line := range []string{
		"requests",
		"requests>=2.0",
		"requests~=2.0",
		"requests!=2.0",
	} {
		if _, err := parsePythonRequirementNames([]byte(line)); err == nil {
			t.Fatalf("expected unpinned requirement %q to be rejected", line)
		}
	}
}

func TestValidatePythonRequirementsRejectsVCSAndURLAndPath(t *testing.T) {
	for _, line := range []string{
		"git+https://github.com/example/pkg.git",
		"https://example.com/pkg.whl",
		"pkg @ https://example.com/pkg.whl",
		"pkg @ /local/path",
		"./local-package",
	} {
		if _, err := parsePythonRequirementNames([]byte(line)); err == nil {
			t.Fatalf("expected VCS/URL/path requirement %q to be rejected", line)
		}
	}
}

func TestParsePythonRequirementNamesStripsExtrasAndMarkers(t *testing.T) {
	data := []byte("Requests==2.31.0\nnumpy[extra]==1.26.4 ; python_version >= \"3.8\"\n")
	names, err := parsePythonRequirementNames(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"requests", "numpy"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("got %#v, want %#v", names, want)
	}
}

func TestEnforcePackagePolicyDeny(t *testing.T) {
	err := enforcePackagePolicy([]string{"requests"}, nil, []string{"requests"})
	if err == nil {
		t.Fatal("expected denied package to be rejected")
	}
}

func TestEnforcePackagePolicyAllowlist(t *testing.T) {
	if err := enforcePackagePolicy([]string{"requests"}, []string{"requests"}, nil); err != nil {
		t.Fatalf("expected allowlisted package to pass, got: %v", err)
	}
	if err := enforcePackagePolicy([]string{"numpy"}, []string{"requests"}, nil); err == nil {
		t.Fatal("expected package not on allowlist to be rejected")
	}
}

func TestEnforcePackagePolicyNoLists(t *testing.T) {
	if err := enforcePackagePolicy([]string{"anything"}, nil, nil); err != nil {
		t.Fatalf("expected no policy to pass everything, got: %v", err)
	}
}

func TestValidatePythonRequirementsRejectsOptionLines(t *testing.T) {
	for _, line := range []string{
		"-e .",
		"--editable .",
		"-r other-requirements.txt",
		"-i https://example.com/simple",
	} {
		if _, err := parsePythonRequirementNames([]byte(line)); err == nil {
			t.Fatalf("expected pip option line %q to be rejected", line)
		}
	}
}

// A configured allowlist made only of blank entries is still an allowlist:
// it must stay default-deny rather than read as "no allowlist configured".
func TestEnforcePackagePolicyBlankAllowlistStaysDefaultDeny(t *testing.T) {
	if err := enforcePackagePolicy([]string{"requests"}, []string{""}, nil); err == nil {
		t.Fatal("expected blank-only allowlist to reject every package")
	}
}

func TestPackagePolicyCanonicalizesPythonNames(t *testing.T) {
	for _, name := range []string{"Demo_Pkg", "demo.pkg", "demo---pkg"} {
		if err := enforcePackagePolicy([]string{name}, nil, []string{"DEMO-pkg"}); err == nil {
			t.Fatalf("deny bypass for %q", name)
		}
		if err := enforcePackagePolicy([]string{name}, []string{"DEMO-pkg"}, nil); err != nil {
			t.Fatalf("equivalent allow name %q rejected: %v", name, err)
		}
	}
}
