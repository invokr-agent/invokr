# Per-model request options

Open **Ctrl+O → Model options** (Ctrl+Left from the first tab). The heading
identifies the current session model. Every field shows its effective value and
resolution source. Edit the override and choose Save; Escape or Cancel does not
save. Saving updates subsequent main requests and the existing `config.json`.
An in-flight request retains its snapshot. Other running sessions pick up file
changes after restart.

| Config key | Accepted override | Environment | Request mapping |
| --- | --- | --- | --- |
| `max_tokens` | Positive integer up to 2147483647, or `omit` | Existing `INVOKR_N_PREDICT` | `MaxOutputTokens`, serialized as `max_completion_tokens` |
| `temperature` | Finite number from 0 to 2, or `omit` | Existing `INVOKR_TEMPERATURE` | `Temperature`; zero is explicit, not unset |
| `top_p` | Finite number from 0 to 1, or `omit` | `INVOKR_TOP_P` | `TopP` |

Blank means **inherit**, and removes that model's override on Save. `omit` means
**do not send this request field**. Config values are strings so these states
remain distinct from numeric zero. Provider-specific restrictions can be narrower
than the panel's validation; the panel does not promise server support.

```json
{
  "agent": {
    "model_options": {
      "my-model": {
        "max_tokens": "32768",
        "temperature": "0",
        "top_p": "omit"
      }
    }
  }
}
```

Keys use exact session model IDs, including aliases; different providers using
the same ID share the override. Saving one model preserves other models and
unknown/unrelated configuration fields. Failed validation or persistence leaves
the running session unchanged. Temporary environment values are never saved.

## Defaults and precedence

No model overrides means existing behavior. In the rebased target `dev-fix-analysis` (`d0ae946`),
`agent.n_predict` defaults to **16384**, and the router sends it as
`max_completion_tokens`. Earlier notes saying “no max_tokens” missed that field.
This change does not remove the existing cap or introduce a raw `max_tokens`
field. Explicit `max_tokens: "omit"` is the opt-in way to omit the cap.

Existing startup resolution supplies inherited values (defaults, file, profile,
environment and CLI flags). Per-model values override that baseline unless the
option's environment variable is nonempty. Existing temperature/output-cap
environment settings retain their already-resolved startup values, including
flag handling. `INVOKR_TOP_P` is new and validated explicitly. Startup
`agent.temperature_disabled`, including `INVOKR_TEMPERATURE_DISABLED`, always
suppresses temperature, even when a model has a numeric override.

The displayed source distinguishes model config, the overriding environment
variable, disabled-temperature policy, registry defaults, and inherited startup
settings. Inherited startup values are not falsely attributed to a particular
file: that existing config path does not retain full file/profile/flag provenance.

These options apply only to main model turns, including main-turn retries, not
to summarizer/embedding calls. Total output includes hidden reasoning, visible
text and tool arguments; it is **not a separate thinking-only budget**. Small
caps can truncate reasoning or tool JSON. No defaults, retry limits, reasoning
effort policy or context thresholds are changed. Existing Reasoning remains on
the Session tab.

## Extending the panel

Add one entry to `config.ModelOptionRegistry`: key, label, type, environment
variable, numeric bounds or `Choices`, optional default, `Read`, and `Apply`.
`Default: ""` inherits the existing request unchanged. `Read` formats the
effective request value; `Apply` receives a validated nonempty value, including
explicit `omit`. Use `Choices` for finite enums or `true`/`false` values. The
generic form, validation, per-model persistence and request traversal need no
new UI branches. Add the new field's mapping/omission test.

For example, another numeric provider field can reuse `optionNumber` and
`optionNumericValue` in its `Read`/`Apply` functions, just as `top_p` does.
Keep request mutations isolated: clone shared maps before adding `Extra` fields.
An option requiring a new provider protocol or agent-loop behavior still needs
that behavior implemented in its existing owner; a registry entry cannot invent
support for continuation or provider-specific reasoning effort.

## Validation

Tests cover bounds/nonfinite values/unknown keys, raw-file preservation, explicit
omission versus inheritance, serialized wire fields, model isolation, environment
precedence, zero temperature, failed-save atomicity, model switching, main versus
summary limits, concurrent saves/requests, and Ctrl+O tab/save/cancel navigation.
The menu test adds a synthetic registry choice to verify automatic field creation.
Tests use scripted providers and random-port HTTP fixtures, never the shared
model server.
