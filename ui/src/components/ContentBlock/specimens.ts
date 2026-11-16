// CompTurns "ContentBlock": the sheet's own content, shared by the stories and the parity sheet.
import type { ContentBlockProps } from "./ContentBlock";

export const pasted: ContentBlockProps = {
  label: "Pasted into prompt",
  meta: "18.4 KB · 412 lines · .csv",
  content: { lines: ["id,email,plan,created_at", "1041,ana@…,team,2026-09-02", "1042,raj@…,pro,2026-09-02", "1043,li@…,team,2026-09-03"], total: 412, bytes: 18_400, from: "head" },
  rawLabel: "full text in raw request",
};
export const toolResult: ContentBlockProps = {
  label: "Tool result · Bash npm test",
  meta: "212 KB · 3,880 lines · exit 1",
  content: { lines: ["  ✓ refresh waits on in-flight promise (12 ms)", "  ✕ concurrent refresh writes once", "    Expected: 1  Received: 2", "Tests: 1 failed, 61 passed"], total: 3_880, bytes: 212_000, from: "tail" },
  rawLabel: "raw response",
};
export const reply: ContentBlockProps = {
  label: "Reply",
  meta: "3.4k tokens · 212 lines",
  content: {
    lines: [
      "Fixed: two refreshes could race and both write the cache.",
      "The second now awaits the first’s promise. I also added a",
      "test that fires two refreshes at once. Remaining work:",
      "1. The observer still retries 401s without backoff…",
    ],
    total: 212,
    bytes: 9_000,
    from: "head",
  },
  note: "head 4 lines · expand shows all, rendered as markdown",
};
export const metadataOnly: ContentBlockProps = {
  label: "Tool result · Bash npm",
  meta: "exit 1 · 18 s",
  content: null,
  note: "program and exit code from Claude Code’s analytics",
};
