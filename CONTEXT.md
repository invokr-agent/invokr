# Invokr Domain Context

## Glossary

### Tool Outcome

Authoritative runtime result of one attempted tool call. Successful attempts increment
the call count; failed and denied attempts are retained in stable execution order.

### Tool Failure

A failed or denied tool attempt. Later attempts do not erase it, including later calls
through the same tool name.

### Tool Outcome Guard

Deterministic notice rendered by user-facing adapters after model prose whenever tool
failures exist. Structured-output mode suppresses the notice in the message while keeping
structured failures and attempted-call count available to callers.

### Sandbox Run

An agent invocation whose entire process tree (agent, subagents, tools) executes inside
a container boundary. Indistinguishable from a direct run at the session level: same
memory, same workspace, same config.

### Job

A stored, named schedule (cron expression plus prompt) owned by Invokr's database
regardless of what executes it. Deleting the record purges every trace, including any
system crontab lines it created.

### Job Runner

Where a Job fires: `session` (Invokr's own scheduler process) or `system` (host cron
invoking Invokr).

### Agent Definition

A named, user-authored profile for a subagent (markdown file with front matter):
which model it uses, how many turns it may take, which tools it may see, and its
standing instructions. Delegating without naming one yields the generalist subagent.

### Approval Queue

The set of tool-approval requests currently awaiting a user decision, reviewed and
decided as a batch (per item or all at once) rather than one blocking prompt at a time.

### Relay

An external program (agent-relay, v0.1.0) that carries Telegram, Signal and Matrix
messages to an `invokr acp` agent over stdio. invokr itself speaks none of them.

### Model Fallback

Startup-only substitution of an unavailable configured model with the provider's first
listed model. Applied once at bootstrap before any session is constructed; never
triggered mid-conversation.

### Model Switch

Session-scoped model change applied at the turn boundary, never persisted. Affects only
the current session for the remainder of its lifetime.

