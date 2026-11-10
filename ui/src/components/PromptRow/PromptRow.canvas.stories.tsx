// Parity specimens: the canvas's own content for each PromptRow data-spec
// (CompRows.dc.html "prompt · …"), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import type { ReactNode } from "react";
import { AgentRowsSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { PromptRow, type PromptItem } from "./PromptRow";
import { ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";

// Staging: CompRows draws each row alone (no table, so no hairline) in a 1180px frame.
// CompTableHover draws its rows in a 1344px frame.
// `table`: CompR11 draws the row in its table, with the table's hairline under it.
const P = ({ spec, frame = 1180, table = false, children }: { spec: string; frame?: number; table?: boolean; children: ReactNode }) => (
  <div data-parity={spec} className={`grid items-start ${table ? "dt-grid" : ""}`} style={{ gridTemplateColumns: `${frame}px` }}>
    {children}
  </div>
);

const NOW = "2026-11-04T12:00:00"; // only the in-flight elapsed time reads it
const base: PromptItem = {
  type: "prompt",
  steps: [],
  prompt: {
    promptId: "p18",
    index: 18,
    t: "2026-11-03T10:25:00",
    durationMs: 291_000,
    text: { retained: false, text: null, chars: 96, pastedBytes: null, images: 0 },
    steps: 22,
    tokensIn: 39_000,
    tokensOut: 2_600,
    toolCounts: { Bash: 19, Read: 2 },
    friction: { frustrated: false, interrupted: false, destructive: 0 },
    inFlight: false,
    turnReport: null,
    hidden: {},
  },
};
const I = (over: Partial<PromptItem["prompt"]>): PromptItem => ({ ...base, prompt: { ...base.prompt, ...over } });
const expanded = I({
  promptId: "p19",
  index: 19,
  t: "2026-11-03T10:42:00",
  durationMs: 100_000,
  text: { retained: true, text: "fix the flaky auth test", chars: 23, pastedBytes: null, images: 0 },
  steps: 9,
  tokensIn: 41_000,
  tokensOut: 3_200,
  toolCounts: { Bash: 4, Read: 3, Edit: 2 },
  friction: { frustrated: true, interrupted: false, destructive: 0 },
});
const arrived = I({
  promptId: "p63",
  index: 63,
  t: "2026-11-06T14:20:00",
  durationMs: 192_000,
  text: { retained: false, text: null, chars: 118, pastedBytes: null, images: 0 },
  steps: 14,
  tokensIn: 52_000,
  tokensOut: 4_100,
  toolCounts: { Bash: 9, Read: 4, Edit: 1 },
  friction: { frustrated: true, interrupted: false, destructive: 0 },
});
const inFlight = I({
  promptId: "p34",
  index: 34,
  t: "2026-11-04T10:05:00",
  durationMs: null,
  text: { ...base.prompt.text, chars: 120 },
  steps: 5,
  tokensIn: 18_000,
  tokensOut: 1_100,
  toolCounts: { Bash: 3, Read: 1 },
  inFlight: true,
});
const interrupted = I({
  promptId: "p20",
  index: 20,
  t: "2026-11-03T11:26:00",
  durationMs: 130_000,
  text: { ...base.prompt.text, chars: 41 },
  steps: 12,
  tokensIn: 52_000,
  tokensOut: 1_900,
  toolCounts: { Bash: 9, Read: 2 },
  friction: { frustrated: false, interrupted: true, destructive: 0 },
});

const meta = { title: "Canvas/Rows/PromptRow", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="PromptRow/collapsed"><PromptRow item={base} expanded={false} onToggle={fn()} now={NOW} /></P>
      <P spec="PromptRow/expanded"><PromptRow item={expanded} expanded onToggle={fn()} now={NOW} /></P>
      <P spec="PromptRow/inFlight"><PromptRow item={inFlight} expanded onToggle={fn()} now="2026-11-04T10:09:03" /></P>
      <P spec="PromptRow/interrupted"><PromptRow item={interrupted} expanded={false} onToggle={fn()} now={NOW} /></P>
      <P spec="PromptRow/hoverLinked" frame={1344}><PromptRow item={expanded} expanded={false} onToggle={fn()} now={NOW} data-force="hover" /></P>
      {/* CompR11: landed on from a timeline tick. */}
      <P spec="PromptRow/arrived" frame={1342} table><PromptRow item={arrived} expanded arrived onToggle={fn()} now={NOW} /></P>
      {/* CompAgents: a Codex turn (§11J gaps 1, 6). */}
      <AgentRowsSheet />
      {/* CompProviders (§11L). */}
      <ProvidersSessionSheet />
    </div>
  ),
};
