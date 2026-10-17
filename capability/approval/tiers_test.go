package approval

import "testing"

func TestEncodePolicyRejectsReservedTiers(t *testing.T) {
	for _, tier := range []string{"allow", "deny", " allow ", "read-only,allow"} {
		if got, err := EncodePolicy(false, []string{tier}); err == nil {
			t.Fatalf("EncodePolicy(--allow %q) = %q, want error", tier, got)
		}
	}
	if got, _ := EncodePolicy(true, nil); !ParsePolicy(got).Approves(Request{Tier: "anything"}) {
		t.Fatal("--allow-all must approve everything")
	}
	got, err := EncodePolicy(false, []string{"read-only"})
	if err != nil || ParsePolicy(got).Approves(Request{Tier: "write_workspace"}) {
		t.Fatalf("read-only policy %q (%v) approved a write", got, err)
	}
	if ParsePolicy(PolicyDeny).Approves(Request{Tier: "deny"}) {
		t.Fatal("deny approved a request")
	}
}
