// Parity specimens: the canvas's own content for each WaitingChecklist data-spec
// (CompStates.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ParityTags } from "../../test/ParityTag";
import { WaitingChecklist } from "./WaitingChecklist";

const meta = { title: "Canvas/Chrome/WaitingChecklist", parameters: { layout: "padded" } } satisfies Meta;
export default meta;

const steps = ["stepDone", "stepActive", "stepPending"];

// The canvas list is 880px inside its border.
export const Canvas: StoryObj = {
  render: () => (
    <ParityTags targets={steps.map((spec, i) => ({ spec: `WaitingChecklist/${spec}`, selector: `.waiting-step:nth-child(${i + 1})` }))}>
      <div data-parity="WaitingChecklist/default" className="grid items-start" style={{ gridTemplateColumns: "882px" }}>
        <WaitingChecklist
          checkedAt="2026-11-04T18:00:00Z"
          steps={[
            { status: "done", title: "Proxy running", detail: "done" },
            { status: "active", title: "First request", detail: "active · waiting 4m 12s" },
            { status: "pending", title: "Person identified", detail: "pending" },
          ]}
        />
      </div>
    </ParityTags>
  ),
};
