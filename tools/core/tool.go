package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"strings"
)

type ApprovalDecision struct {
	Request   *approval.Request
	Deny      string
	Automatic bool
}

func Auto() ApprovalDecision { return ApprovalDecision{} }

func Automatic(req approval.Request) ApprovalDecision {
	return ApprovalDecision{Request: &req, Automatic: true}
}

func Ask(req approval.Request) ApprovalDecision { return ApprovalDecision{Request: &req} }

func Deny(reason string) ApprovalDecision { return ApprovalDecision{Deny: strings.TrimSpace(reason)} }

type Tool[A any] struct {
	Spec         ToolSpec
	Decode       func(*ToolCall) (A, error)
	Prepare      func(context.Context, A) (A, error)
	Approve      func(context.Context, A) ApprovalDecision
	Execute      func(context.Context, A) (string, error)
	AuditFailure func(context.Context, A, error)
	Approver     approval.Approver
}

func Register[A any](registrar NativeRegistrar, tool Tool[A]) {
	if registrar == nil {
		return
	}
	if strings.TrimSpace(tool.Spec.Name) == "" {
		panic("tool registration requires spec name")
	}
	if tool.Execute == nil {
		panic(fmt.Sprintf("tool %q registration requires execute", tool.Spec.Name))
	}
	tool.Spec.Metadata.ManagedApproval = true
	registrar.RegisterNative(tool.Spec, tool.Handle)
}

func (t Tool[A]) Handle(ctx context.Context, call *ToolCall) (out string, err error) {
	var args A
	executing := false
	defer func() {
		if err != nil && !executing && t.AuditFailure != nil {
			t.AuditFailure(ctx, args, err)
		}
	}()

	args, err = t.decode(call)
	if err != nil {
		return "", err
	}
	if t.Prepare != nil {
		args, err = t.Prepare(ctx, args)
		if err != nil {
			return "", err
		}
	}
	if err := t.enforceApproval(ctx, args); err != nil {
		return "", err
	}
	executing = true
	return t.Execute(ctx, args)
}

func (t Tool[A]) decode(call *ToolCall) (A, error) {
	if t.Decode != nil {
		return t.Decode(call)
	}
	return DecodeOptionalToolArgs[A](call)
}

func (t Tool[A]) enforceApproval(ctx context.Context, args A) error {
	decision := Auto()
	if t.Approve != nil {
		decision = t.Approve(ctx, args)
	}
	return EnforceApproval(ctx, t.Approver, t.Spec, decision)
}

func EnforceApproval(ctx context.Context, approver approval.Approver, spec ToolSpec, decision ApprovalDecision) error {
	if decision.Deny != "" {
		return &approval.DeniedError{Tool: spec.Name, Reason: decision.Deny}
	}
	req := approval.CallRequest(ctx)
	if decision.Request != nil {
		req = *decision.Request
	}
	if handled, err := approval.Authorize(ctx, approver, req, decision.Request != nil && !decision.Automatic); handled {
		return err
	}
	if decision.Request == nil || decision.Automatic {
		return nil
	}
	if approver == nil {
		return missingApproverError(spec, *decision.Request)
	}
	_, err := approver.RequestApproval(ctx, *decision.Request)
	return err
}

func missingApproverError(spec ToolSpec, req approval.Request) error {
	toolName := req.Tool
	if strings.TrimSpace(toolName) == "" {
		toolName = spec.Name
	}
	msg := fmt.Sprintf("tool %q requires approval", toolName)
	// The request's own tier names the policy; the spec's declared approval
	// policy is the fallback for a request that left it blank.
	if policy := cmp.Or(strings.TrimSpace(req.Tier), strings.TrimSpace(spec.Metadata.Policy.Approval)); policy != "" {
		msg += " for policy " + policy
	}
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		msg += ": " + reason
	}
	return errors.New(msg)
}
