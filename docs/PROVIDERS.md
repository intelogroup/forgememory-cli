# Forge LLM Provider & Model Matrix

To prevent misconfigurations and API errors during session distillation, Forge validates the configured combination of `FORGE_PROVIDER`, `FORGE_MODEL`, and `FORGE_BASE_URL`.

## Supported Configuration Matrix

| Provider Name (`FORGE_PROVIDER`) | Default Model (`FORGE_MODEL`) | Default Base URL (`FORGE_BASE_URL`) | Supported Models / Constraints |
| :--- | :--- | :--- | :--- |
| **`forgememo`** *(Default)* | `claude-haiku-4-5-20251001` | `https://forgememo-server.onrender.com/api/forge` | Claude models only (`claude-*`) |
| **`anthropic`** | `claude-haiku-4-5-20251001` | `https://api.anthropic.com` | Claude models only (`claude-*`) |
| **`openai`** | `gpt-4o` | `https://api.openai.com` | OpenAI models only (`gpt-*`, `o1-*`, `o3-*`) |
| **`groq`** | `llama-3.3-70b-versatile` | `https://api.groq.com/openai` | Open source models (e.g. `llama-3.3-70b-versatile`, `gemma2-9b-it`) |
| **`nvidia`** | `meta/llama-3.3-70b-instruct` | `https://integrate.api.nvidia.com/v1` | Open source and proprietary models (e.g. `nemotron-340b`) |
| **`ollama`** | `llama3:latest` | `http://localhost:11434` | Open source models locally pulled (e.g. `llama3`, `gemma2`) |
| **`antigravity`** | `flash` | *N/A* | `flash`, `flash_lite`, `pro` |

---

## Validation Behavior

Forge checks for configuration compatibility in two places:
1. **Interactive Config Execution**: When running `forge config`, mismatches are checked locally before any network calls, prompting immediate correction.
2. **Daemon/Distillation Loading**: Every time configuration is loaded (e.g., in background loops or daemon startups), Forge prints a warning to `stderr` if it detects incompatible pairings (e.g., attempting to run a `gpt-` model on `ollama`, or a `gemini` model on `openai`).
