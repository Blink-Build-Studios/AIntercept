// Parity specimens: the canvas's own content for each AgentCard data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { AgentCard } from "./AgentCard";
import { ProvidersPeopleSheet } from "../ProviderMark/CompProviders.canvas.stories";

// The canvas lays the cards out four to a row: 299px each.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: "299px" }}>
    {children}
  </div>
);
const noop = () => {};

const meta = { title: "Canvas/Summary/AgentCard", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      <P spec="AgentCard/default">
        <AgentCard agent="claude-code" people={3} summary="18 sessions · exact tokens" selected={false} onClick={noop} />
      </P>
      <P spec="AgentCard/hover">
        <AgentCard agent="codex" people={1} summary="7 sessions · ≈ tokens" selected={false} onClick={noop} data-force="hover" />
      </P>
      <P spec="AgentCard/selected">
        <AgentCard agent="chatgpt" people={2} summary="no token counts" selected onClick={noop} />
      </P>
      <P spec="AgentCard/focus">
        <AgentCard agent="claude-ai" people={1} summary="4 conversations" selected={false} onClick={noop} data-force="focus" />
      </P>
      {/* CompProviders (§11L). */}
      <ProvidersPeopleSheet />
    </div>
  ),
};
