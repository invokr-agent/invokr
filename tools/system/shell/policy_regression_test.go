package shell

import "testing"

// TestPolicyNeverAutoApprovesStateMutation is the regression table for the
// auto-approval boundary: every line here reached the read-only tier at some
// point during review, and read-only means no human sees the command. Three
// review passes found sixteen of them, all the same shape -- a construct that
// sets a variable or moves the cwd without routing through a gate -- so the
// table is seeded with every spelling found rather than one per class. The
// benign half is load-bearing: refusing everything would also pass the top
// half, and a shell that refuses ordinary lines is the failure this policy
// replaced.
func TestPolicyNeverAutoApprovesStateMutation(t *testing.T) {
	attacks := []string{
		// round 1+2 (regression)
		`trap 'touch /tmp/pwned' EXIT; ls`, `hash -p ./x ls; ls`, `PATH=/tmp/evil; ls`,
		`export PATH=/tmp/evil; ls`, `declare -x PATH=/tmp/evil; ls`, `typeset PATH=/tmp/evil; ls`,
		`readonly PATH=/tmp/evil; ls`, `local PATH=/tmp/evil; ls`, `IFS=x; ls`,
		`ls >& out.txt`, `cat $'\x2f\x65\x74\x63\x2fpasswd'`,
		`declare -n r=PATH; r=/tmp/evil; ls`, `typeset -n r=PATH; r=/tmp/evil; ls`,
		`printf -v PATH /tmp/evil; ls`, `read -r PATH <<< /tmp/evil; ls`, `getopts x PATH; ls`,
		`unset PATH; echo ${PATH:=/tmp/evil}; ls`, `x='$(touch /tmp/pwn)'; echo "${x@P}"`,
		`cat {/etc/,}passwd`, `cat ~/.ssh/id_rsa`, `cat < ~/.ssh/id_rsa`, `coproc ls`,
		// round 3 (fable)
		`for PATH in .; do ls; done`,
		`select PATH in /tmp; do ls; done`,
		`read -aPATH; ls`,
		`printf -v 'PATH[0]' /tmp; ls`,
		`read 'PATH[0]'; ls`,
		`cd -- /etc; cat passwd`,
		`cd -P /etc; cat passwd`,
		`x=/etc; cd $x; cat passwd`,
		`HOME=/etc; cd; cat passwd`,
		`cd -; cat passwd`,
	}
	for _, line := range attacks {
		pol, err := classify(t, line)
		if err == nil && pol.tier == shellTierReadonly {
			t.Errorf("BYPASS OPEN (auto-approved): %q", line)
		}
	}
	safe := []string{
		`ls`, `ls -la`, `ls 2>&1`, `ls >&2`, `ls >&-`, `N=3 ls`, `X=1; ls`,
		`set -a; X=1; ls`, `echo hi | grep h`, `cat file.txt`, `wc -l < f`,
		`git status && git log -3`, `for f in *.go; do cat $f; done`,
		`for i in 1 2; do echo $i; done`, `cd sub && ls`, `cd ./sub && cat f`,
		`read -r line; echo $line`, `printf -v out x; echo $out`,
	}
	for _, line := range safe {
		pol, err := classify(t, line)
		if err != nil {
			t.Errorf("FALSE REFUSAL: %q err=%v", line, err)
			continue
		}
		if pol.tier != shellTierReadonly {
			t.Errorf("FALSE DEMOTION: %q tier=%v", line, pol.tier)
		}
	}
}
