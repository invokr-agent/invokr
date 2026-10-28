package testutil

import (
	"cmp"
	"context"
	"strings"

	"github.com/invokr-agent/invokr/capability/approval"
)

type StaticApprover struct {
	Decision approval.Decision
}

func (s StaticApprover) RequestApproval(_ context.Context, req approval.Request) (approval.Decision, error) {
	if s.Decision.Approved {
		return s.Decision, nil
	}
	reason := cmp.Or(strings.TrimSpace(s.Decision.Reason), "denied by policy")
	return s.Decision, &approval.DeniedError{Tool: req.Tool, Reason: reason}
}
