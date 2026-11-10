// Parity specimens: the canvas's own content for each AppBar data-spec
// (CompStates.dc.html, CompDensity.dc.html, CompR11.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { UpdatedAgo } from "../UpdatedAgo/UpdatedAgo";
import { AppBar } from "./AppBar";
import { ProvidersAppBarSheet } from "../ProviderMark/CompProviders.canvas.stories";

// The canvas specimens span the 1344px sheet column.
const P = ({ spec, children }: { spec: string; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start" style={{ gridTemplateColumns: "1344px" }}>
    {children}
  </div>
);
const about = () => undefined;

export default { title: "Canvas/Chrome/AppBar", parameters: { layout: "fullscreen" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <P spec="AppBar/personPage">
        <AppBar
          breadcrumb={[{ label: "people", href: "#people" }, { label: "name unknown", agents: ["claude-code"] }]}
          window={{ options: ["24h", "7d", "14d"].map((w) => ({ value: w, label: w })), value: "14d", onChange: () => {}, "aria-label": "Time window" }}
          live={{ status: "live", intervalS: 5 }}
          onPause={() => {}}
          onAbout={about}
        />
      </P>
      <P spec="AppBar/peopleWindow">
        <AppBar
          breadcrumb={[{ label: "people" }]}
          window={{ options: ["24h", "7d", "14d", "All"].map((w) => ({ value: w, label: w })), value: "14d", onChange: () => {}, "aria-label": "Active in the last" }}
          windowLabel="Active in the last"
          updated={<UpdatedAgo at="2026-11-08T11:32:05Z" now="2026-11-08T11:32:17Z" />}
          onAbout={about}
        />
      </P>
      <P spec="AppBar/brand">
        <AppBar breadcrumb={[{ label: "people", href: "#people" }, { label: "name unknown" }]} onAbout={about} />
      </P>
      <P spec="AppBar/loading">
        <AppBar breadcrumb={[{ label: "people" }]} live={{ status: "live" }} loading onAbout={about} />
      </P>
      {/* CompProviders (§11L). */}
      <ProvidersAppBarSheet />
    </div>
  ),
};
