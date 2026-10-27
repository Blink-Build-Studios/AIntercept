# extractor

Turns a captured `events.ndjson` (from the observer in full-capture mode) into
normalized cross-provider usage records focused on **tool visibility** — which
app used which MCP server and which tools, on which model.

This is a dev/analysis tool. It runs on a raw local capture (which contains real
prompt content, paths, and credentials) and is **never** part of the production
proxy path. Raw captures and extracted output live under `../data/` and are
gitignored. Only code and *sanitized* fixtures are committed.

## Usage

```bash
# 1. Normalize a capture into one record per transaction (common schema):
python3 extractor/extract.py data/events.ndjson -o data/extracted.ndjson

# 2. Cross-provider tool-visibility report (servers -> tools -> counts):
python3 extractor/report.py data/extracted.ndjson

# 3. Make committable fixtures (strips content/identity, keeps shape):
python3 extractor/sanitize.py data/extracted.ndjson > extractor/fixtures/anthropic_messages.sample.ndjson
```

- `extract.py` — request/response → normalized records. See `SCHEMA.md`.
- `report.py` — aggregate server→tool→count view and app→MCP-servers mapping.
- `sanitize.py` — drops tool inputs, identity, paths; keeps schema + tool names.
- `analyze_capture.py` (in `../scripts/`) — ad-hoc wire-format exploration.

## Known refinements (follow-ups)

- **Global tool-call dedup:** `report.py` dedups tool calls per transaction, but
  one long session spans many transactions that each echo the full history, so
  raw counts are inflated. For true per-session counts, dedup by tool-call `id`
  across the whole capture.
- **Response-side fields** (`tool_calls`, `usage`, `stop_reason`) require an
  intact (base64) capture; the first capture corrupted gzip'd responses. The
  observer is now fixed (binary-safe base64). Re-capture to populate these.
- **More providers:** OpenAI / Gemini mappings sketched in `SCHEMA.md`.
