package core

import "github.com/invokr-ai/invokr/constants"

func NativeMetadata(namespace string, tiers []string, approval string, capabilities ToolCapabilities) ToolMetadata {
	return ToolMetadata{Namespace: namespace, Source: "native", Policy: ToolPolicy{Tiers: append([]string(nil), tiers...), Approval: approval}, Capabilities: capabilities, AlwaysVisible: true}
}

func ReadWorkspaceMetadata(namespace string, capabilities ToolCapabilities) ToolMetadata {
	return NativeMetadata(namespace, []string{"read_workspace"}, constants.ApprovalPolicyAuto, capabilities)
}

func WriteWorkspaceAskMetadata(namespace string, capabilities ToolCapabilities) ToolMetadata {
	return NativeMetadata(namespace, []string{"write_workspace"}, constants.ApprovalPolicyAsk, capabilities)
}

func NetworkAskMetadata(namespace string, tier string, capabilities ToolCapabilities) ToolMetadata {
	return NativeMetadata(namespace, []string{tier}, constants.ApprovalPolicyAsk, capabilities)
}
