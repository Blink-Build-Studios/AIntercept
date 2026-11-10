// Parity specimen: the canvas's own content for ErrorState/loadFailed
// (CompStates.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { ErrorState } from "./ErrorState";

export default { title: "Canvas/Chrome/ErrorState", parameters: { layout: "padded" } } satisfies Meta;

// The canvas specimen fills a 616px column.
export const Canvas: StoryObj = {
  render: () => (
    <div data-parity="ErrorState/loadFailed" className="inline-grid items-start p-4" style={{ gridTemplateColumns: "616px" }}>
      <ErrorState
        title="Couldn’t load this session"
        body="The query timed out after 30 s. Capture isn’t affected: the proxy keeps recording."
        meta="11:31:02 · upstream timeout · query q_8f…"
        onRetry={() => {}}
        back={{ label: "← Back to person", href: "#person" }}
      />
    </div>
  ),
};
