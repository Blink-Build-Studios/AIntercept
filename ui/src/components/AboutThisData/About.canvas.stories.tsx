// Parity specimens from CompProviders.dc.html (handoff §11M 3.1, 3.2): About this data's
// Products section and What is kept without recorded bodies.
import type { Meta, StoryObj } from "@storybook/react";
import type { ReactNode } from "react";
import { ParityTags } from "../../test/ParityTag";
import { AboutProduct, AboutSection, RetentionList, type AboutProductData } from "./AboutThisData";
import "./AboutThisData.css";

const products: (AboutProductData & { spec: string })[] = [
  { spec: "AboutProduct/claudeCode", agent: "claude-code", tokenAccuracy: "exact", sessions: 18 },
  { spec: "AboutProduct/codex", agent: "codex", tokenAccuracy: "exact", sessions: 7 },
  { spec: "AboutProduct/chatgpt", agent: "chatgpt", tokenAccuracy: "missing", sessions: 6 },
  { spec: "AboutProduct/claudeAi", agent: "claude-ai", tokenAccuracy: "estimated", sessions: 0 },
];

const P = ({ spec, width, children }: { spec: string; width: number; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: `${width}px` }}>
    {children}
  </div>
);

export function AboutSheet() {
  return (
    <div className="flex flex-col items-start gap-4 p-4">
      <ParityTags targets={[{ spec: "AboutSection/products", selector: ".about-table" }, ...products.map((p, i) => ({ spec: p.spec, selector: `.about-product:nth-child(${i + 1})` }))]}>
        <div className="grid items-start" style={{ gridTemplateColumns: "608px" }}>
          <AboutSection title="Products">
            <div className="about-table bg-surface border border-line">
              {products.map(({ spec, ...p }) => (
                <AboutProduct key={spec} product={p} />
              ))}
            </div>
          </AboutSection>
        </div>
      </ParityTags>
      <P spec="RetentionList/metadataOnly" width={608}>
        <RetentionList contentRetained={false} />
      </P>
    </div>
  );
}

const meta = { title: "Canvas/About/AboutSection", parameters: { layout: "fullscreen" }, excludeStories: ["AboutSheet"] } satisfies Meta;
export default meta;
export const Canvas: StoryObj = { render: () => <AboutSheet /> };
