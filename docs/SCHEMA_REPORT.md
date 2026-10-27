# Unrecognized-shape reporting (`schemareport`)

The observer captures a **structure-only fingerprint** of every transaction it
did *not* fully recognize (no model **or** no token usage extracted) and writes
it to the object store under a `schema-samples/` prefix. `schemareport` reads
those samples and prints a **redacted report** of the distinct endpoint/body
shapes we don't yet handle — so coverage can be extended **without anyone handing
over sensitive data or environment access**.

## Why it exists

The intended hand-off: someone runs the stack in *their own* account and, when
asked "what's flowing through that we don't understand yet?", runs one command
and sends back the JSON. The report contains structure only:

- **JSON bodies** → a skeleton where every object **key is kept** but every scalar
  **value is replaced by its type** (`str(<=64)`, `int`, `bool`, …). No value is
  ever present.
- **Non-JSON bodies** → a classification (`sse`/`text`/`binary`/`empty`) + length.
  Compressed bodies are `binary` (never decoded).
- **Headers** → names only, except a small allowlist of non-secret SDK/version
  markers whose values are kept. The sensitive set (authorization, cookies, …) is
  never present.
- Plus routing metadata (host/method/path/status) and the pseudonymous `key_hash`.

With `--redact-keys`, object **keys** are scrubbed to length buckets too, for a
report that carries nothing identifying at all — structure only.

## Usage

`schemareport` reuses the **same `OBSERVER_S3_*` environment** the observer uses,
so point it at the same bucket/credentials:

```sh
export OBSERVER_S3_BUCKET=your-telemetry-bucket
export OBSERVER_S3_REGION=nyc3
export OBSERVER_S3_ENDPOINT=https://nyc3.digitaloceanspaces.com   # DO Spaces / MinIO; omit on AWS
export OBSERVER_S3_ACCESS_KEY_ID=...                              # omit on AWS (uses IRSA)
export OBSERVER_S3_SECRET_ACCESS_KEY=...
# export OBSERVER_S3_PREFIX=aintercept                             # if the sink used a prefix

# From the repo root:
make schema-report                      # full report as JSON to stdout
make schema-report ARGS="--format text" # human summary
make schema-report ARGS="--redact-keys" # also scrub object keys

# Or directly:
cd observer && go run ./cmd/schemareport --format json --out report.json
```

### Incremental runs (don't resend the same data)

Every run prints a watermark on stderr:

```
schemareport: scanned 12 objects, 48 samples, 3 distinct endpoint shapes. Next run: --since 1730000000000000000
```

Pass that integer back as `--since` next time to get **only new shapes** since the
last run:

```sh
make schema-report ARGS="--since 1730000000000000000"
```

`--since` also accepts an RFC3339 timestamp (`--since 2026-10-24T00:00:00Z`). The
watermark is last-modified/timestamp based and is also used to skip old objects
before downloading them, so repeat runs are cheap.

## Report shape

```jsonc
{
  "generated_at": "2026-10-24T...Z",
  "watermark_ns": 1730000000000000000,   // pass as --since next run
  "samples_scanned": 48,
  "objects_scanned": 12,
  "keys_redacted": false,
  "endpoints": [
    {
      "host": "api.newprovider.example",
      "method": "POST",
      "path": "/v2/infer",
      "reason": "no_model,no_usage",       // which extractor(s) missed
      "count": 30,
      "statuses": { "200": 28, "429": 2 },
      "models": [],                          // distinct models seen, when any
      "safe_headers": { "user-agent": "newprovider-sdk/1.2.3" },
      "request_header_names": ["authorization", "content-type", ...],
      "request_shapes": [
        { "kind": "json", "count": 30,
          "skeleton": { "model": "str(<=64)", "input": { "prompt": "str(>4k)" } } }
      ],
      "response_shapes": [ ... ]
    }
  ]
}
```

Endpoints are ordered busiest-first, so the shapes worth adding capture for are at
the top. To extend coverage, map the skeleton's keys to the extractor in
`observer/internal/events/` (model via `usage.go`/`ModelFromPath`, usage via
`applyUsageJSON`, tools via `tools.go`).
