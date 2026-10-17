# JSON profiles

Setup creates editable JSON profiles in `~/.invokr/profiles/`. It preserves edits
on later runs. To restore the shipped files, choose **Reset JSON profiles** in
`invokr setup` or **Ctrl+O → Setup**. Reset replaces the three shipped JSON files,
leaving Markdown profiles and other JSON files alone.

Select one with `invokr --profile researcher` for one run or
`invokr config use researcher` to persist it. A JSON profile can set `provider`, `model`, `temperature`,
`system_prompt`, `tools.allowlist`, and `mode` (`manual`, `auto`, or `plan`). Fields
left out inherit the base config. Command-line flags still override profile
values. Markdown profiles continue to work and take priority over a JSON file
with the same name. The Profiles menu can switch model and prompt live; use
`config use` or `--profile` to apply tools, mode, provider, and temperature on startup.

| Profile | Available tools | Approval mode |
| --- | --- | --- |
| `researcher.json` | File reads and web search/fetch | Auto |
| `planner.json` | Researcher tools plus in-memory `todo` | Plan |
| `coder.json` | All configured tools (the existing default) | Manual |

The existing caller checks the allowlist both when advertising tools and when
dispatching calls, including calls restored from a session. An empty allowlist
means all tools. `tools` remains available for tool management; it cannot enable
tools outside a nonempty allowlist.

`web_search` may write its cache or start a local SearXNG container under
existing settings, so the researcher profile is tool-limited, not a strict
filesystem read-only sandbox. Auto does not bypass the allowlist or sandbox.
`file_search` is excluded from researcher and planner because its Git metadata
lookup can execute repository-configured hooks; use Manual mode when needed.
