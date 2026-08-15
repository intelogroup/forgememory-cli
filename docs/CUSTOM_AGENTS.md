# Custom runtime agents (e.g. Clixen)

`forge hook` accepts any `--source` value — it is not restricted to the
built-in adapters (`claude`, `codex`, `gemini`, `opencode`, `antigravity`).
A long-running custom runtime agent (its own ingress, orchestrator/tool loop,
model calls, trace store) can emit events into ForgeMemo the same way,
without impersonating a built-in agent:

```sh
echo '{"session_id":"...","event_type":"PostToolUse", ...}' | \
  forge hook --source clixen --event PostToolUse
```

If the daemon is unreachable, the hook enqueues the event locally for replay
— no different from the built-in adapters.

## Field mapping

| Forge field       | Clixen source                                  |
|--------------------|------------------------------------------------|
| `project_id`       | `clixen`                                        |
| `source_tool`      | `clixen`                                        |
| `session_id`       | stable Clixen conversation/runtime session ID   |
| `trace_id`         | Clixen run ID                                   |
| `span_id`          | run ID + step sequence                          |
| `event_type`       | `UserPromptSubmit`, `PostToolUse`, `Stop` (or documented runtime equivalent) |
| `tool_name`        | Clixen tool name                                |
| `duration_ms`      | step duration                                   |
| `status`           | step outcome                                    |
| `model`            | model used for the step                         |
| `payload`          | redacted tool arguments/result summary          |

Redact WhatsApp message bodies, phone numbers, credentials, and full tool
output before emission — `forge hook` does not know the shape of a custom
agent's payload and cannot redact fields it doesn't recognize.

## Observability mode

Default `FORGE_OBSERVABILITY_MODE=minimal` only captures write tools on
`PostToolUse`. For full custom-agent tracing, set `FORGE_OBSERVABILITY_MODE`
to `standard` or `forensic` in the environment `forge hook` runs under.
