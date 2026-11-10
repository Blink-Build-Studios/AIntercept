// Parity specimens: the canvas's own content for each KeyValueList data-spec
// (CompSummary.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { sourceTip } from "../../lib/tips";
import { ParityTags } from "../../test/ParityTag";
import { KeyValueList } from "./KeyValueList";

const MCP_TIP = sourceTip("MCP server names in tool definitions", { n: 9, of: 11, per: "sessions" });

// `track` reproduces the canvas staging: the specimens fill 372px and 272px cells.
const P = ({ spec, track, children }: { spec: string; track: number; children: ReactNode }) => (
  <div data-parity={spec} className="inline-grid items-start p-4" style={{ gridTemplateColumns: `${track}px` }}>
    {children}
  </div>
);

export default { title: "Canvas/Summary/KeyValueList", parameters: { layout: "padded" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-wrap items-start gap-3">
      {/* CompTableHover: a row of the default list hovered, the list 520px inside its border. */}
      <ParityTags targets={[{ spec: "KeyValueList/rowHover", selector: ".kv-row-tip[data-force]" }]}>
        <div className="inline-grid items-start p-4" style={{ gridTemplateColumns: "522px" }}>
          <KeyValueList
            title="Capabilities · seen in any session"
            accent="skills"
            action={{ label: "per session ›", href: "#sessions" }}
            rows={[
              { k: "MCP servers", d: "Gmail, Drive, Vercel +8", v: "11", tip: MCP_TIP, force: "hover" },
              { k: "Skills", d: "owner name + email", v: "87" },
            ]}
          />
        </div>
      </ParityTags>
      <P spec="KeyValueList/default" track={372}>
        <KeyValueList
          title="Capabilities · seen in any session"
          accent="skills"
          action={{ label: "per session ›", href: "#sessions" }}
          rows={[
            { k: "MCP servers", d: "Gmail, Drive, Vercel +8", v: "11" },
            { k: "Skills", d: "owner name + email", v: "87" },
            { k: "Tool calls", d: "not extracted yet", v: null, unknownReason: "not extracted yet" },
          ]}
        />
      </P>
      <P spec="KeyValueList/loading" track={272}>
        <KeyValueList title="Capabilities · seen in any session" rows={[]} loading />
      </P>
    </div>
  ),
};
