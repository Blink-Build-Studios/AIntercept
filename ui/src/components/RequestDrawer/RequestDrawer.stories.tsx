import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { useState } from "react";
import failed from "../../api/samples/request/failed";
import normal from "../../api/samples/request/normal";
import notCaptured from "../../api/samples/request/not_captured";
import retained from "../../api/samples/request/retained";
import detailsNormal from "../../api/samples/request_details/normal";
import rawNormal from "../../api/samples/request_raw/normal";
import rawRetained from "../../api/samples/request_raw/retained";
import rawFailed from "../../api/samples/request_raw/failed";
import rawUnavailable from "../../api/samples/request_raw/unavailable";
import sessionRequests from "../../api/samples/session_requests/many";
import { readyRegion, type Region } from "../../lib/region";
import type { DrawerTab } from "./DrawerTabs";
import { curl, type Detail, type Details, type Raw } from "./drawerData";
import { RequestDrawer } from "./RequestDrawer";

type Args = { detail: Region<Detail>; raw: Region<Raw>; details: Region<Details>; seed?: (typeof sessionRequests.data)[number]; requestId?: string; initialTab?: DrawerTab };

/** The drawer with its tabs working inside the story; the data comes from the samples. */
function Drawer({ detail, raw, details, seed, requestId, initialTab = "summary" }: Args) {
  const [tab, setTab] = useState<DrawerTab>(initialTab);
  const id = requestId ?? detail.data?.requestId ?? "";
  return (
    <RequestDrawer
      requestId={id}
      seed={seed}
      detail={detail}
      raw={raw}
      details={details}
      onRetry={fn()}
      tab={tab}
      onTab={setTab}
      onClose={fn()}
      onNavigate={fn()}
      onRawHover={fn()}
      curl={async () => (raw.data ? curl(raw.data) : "")}
      link={`https://aintercept.local/sessions/s?req=${id}&dtab=${tab}`}
    />
  );
}

const meta = { title: "Components/Drawer/RequestDrawer", component: Drawer, parameters: { layout: "fullscreen" } } satisfies Meta<typeof Drawer>;
export default meta;
type Story = StoryObj<typeof meta>;

const loading = { status: "loading", data: undefined } as const;
const ready = (detail: Detail, raw: Raw = rawNormal.data): Args => ({ detail: readyRegion(detail), raw: readyRegion(raw), details: readyRegion(detailsNormal.data) });

/** A captured model call. */
export const Normal: Story = { args: ready(normal.data) };
/** Reported by Claude Code, never seen by the proxy: Raw has nothing. */
export const NotCaptured: Story = { args: ready(notCaptured.data, rawUnavailable.data) };
/** The connection failed; Claude Code retried. */
export const Failed: Story = { args: ready(failed.data, rawFailed.data) };
/** Experimentation mode: bodies are kept (opens on Raw). */
export const Retained: Story = { args: { ...ready(retained.data, rawRetained.data), initialTab: "raw" } };
/** Opened from a row: the header shows the row while the request loads. */
const row = sessionRequests.data.find((r) => r.kind === "model" && r.status === 200);
export const Loading: Story = { args: { detail: loading, raw: loading, details: loading, seed: row, requestId: row?.requestId } };
export const Error: Story = {
  args: { detail: { status: "error", data: undefined, error: { message: "Query failed", queryId: "q_sample" } }, raw: loading, details: loading, requestId: normal.data.requestId },
};

// A Codex call (handoff §11L.10), from the normal sample with Codex's path and key facts.
const codex: Detail = {
  ...normal.data,
  path: "/backend-api/codex/responses",
  keyStrip: {
    ...normal.data.keyStrip,
    model: "gpt-6.1-sol",
    effort: "medium",
    tokens: { ...normal.data.keyStrip.tokens, input: 31_200, cacheRead: 28_400, cacheWrite: null, output: 640, accuracy: "exact" },
    reasoningTokens: 212,
    ttftMs: null,
    proxyTtftMs: 2600,
    ttftSource: "proxy",
    stopReason: "tool_call",
    usageMissing: null,
  },
};
/** A Codex call: the tiles form (Model · Tokens · Reasoning · TTFT · Duration), no Cost tile. */
export const CodexCall: Story = { args: ready(codex) };
/** A Codex call whose stream was cut: Tokens and Reasoning `—`, `not captured`, the reason in the tooltip. */
export const CodexNoUsage: Story = {
  args: ready({ ...codex, keyStrip: { ...codex.keyStrip, effort: "high", tokens: { ...codex.keyStrip.tokens, input: null, cacheRead: null, output: null, accuracy: "missing" }, reasoningTokens: null, ttftMs: 1900, ttftSource: "client", stopReason: null, usageMissing: "streamCut" } }),
};
