// Parity specimens for the request drawer's parts (CompDrawer.dc.html), with the
// sheet's own content, measured against the canvas by `make parity`. Each part's
// Canvas/Drawer/<Component> story renders this sheet.
import type { Meta, StoryObj } from "@storybook/react";
import type { CSSProperties, ReactNode } from "react";
import { ContentRow } from "./ContentRow";
import { CopyButton } from "../CopyButton/CopyButton";
import { DetailSection } from "./DetailSection";
import { DrawerMeta } from "./DrawerMeta";
import { DrawerTabs } from "./DrawerTabs";
import { FieldList } from "./FieldList";
import { HeaderList } from "./HeaderList";
import { KeyStrip, type KeyStripCellData } from "./KeyStrip";
import { StreamEventList } from "./StreamEventList";
import { TimingWaterfall } from "./TimingWaterfall";
import { ParityTags } from "../../test/ParityTag";
import { DrawerHeader, type DrawerKind } from "./DrawerHeader";

/** The sheet's `.box`: 820px (or `width`) inside a 1px border, on surface. */
const Box = ({ width = 820, sheet, children }: { width?: number; sheet?: string; children: ReactNode }) => (
  <div data-sheet={sheet} className="border border-line bg-surface" style={{ width: width + 2 }}>
    {children}
  </div>
);
/** A specimen wrapper: its first child is measured; `track` is the staging width. */
const P = ({ spec, track, style, children }: { spec: string; track: number; style?: CSSProperties; children: ReactNode }) => (
  <div data-parity={spec} className="grid items-start" style={{ gridTemplateColumns: `${track}px`, ...style }}>
    {children}
  </div>
);
const noop = () => {};

const headers: {
  st: string;
  kind: DrawerKind;
  status: string;
  meta: string[];
}[] = [
  {
    st: "normal",
    kind: "normal",
    status: "200 · 9.3 s · 8.2 KB ↑ · 3.4 KB ↓",
    meta: ["Nov 3 10:42:16.204", "prompt #19", "step 2 of 9", "main", "req_01H…9fa"],
  },
  {
    st: "reportedNotCaptured",
    kind: "notCaptured",
    status: "▲ reported by Claude Code · not captured",
    meta: ["Nov 3 15:22:08", "prompt #22", "requestId from analytics"],
  },
  {
    st: "failed",
    kind: "failed",
    status: "✕ ERR · connection reset after 0.6 s · retried",
    meta: ["Nov 3 10:03:08.412", "prompt #17", "attempt 1 of 2"],
  },
];
const values: KeyStripCellData[] = [
  { label: "Model", value: "opus", sub: "effort high" },
  { label: "Tokens", value: "43.0k → 1.2k", sub: "99% from cache" },
  { label: "Cost est.", value: "$0.21", sub: "API-equivalent" },
  { label: "TTFT", value: "1.9 s", sub: "proxy 1.87 s" },
  { label: "Duration", value: "9.3 s", sub: "47 events" },
  { label: "Stop", value: "tool_use", sub: "1 tool call" },
];
const unknown: KeyStripCellData[] = [
  { label: "Model", value: "opus", sub: "from analytics" },
  { label: "Tokens", value: null, sub: "no response" },
  { label: "Cost est.", value: null, sub: "not billed" },
  { label: "TTFT", value: null, sub: "no first byte" },
  { label: "Duration", value: "0.6 s", sub: "until reset" },
  { label: "Stop", value: null, sub: "not reported" },
];
const cellSpecs = (names: string[], strip: string) =>
  names.map((n, i) => ({
    spec: `KeyStripCell/${n}`,
    selector: `[data-parity="${strip}"] .key-strip-cell:nth-child(${i + 1})`,
  }));
const cellTargets = [
  ...cellSpecs(["model", "tokens", "costEst", "ttft", "duration", "stop"], "KeyStrip/values"),
  ...cellSpecs(["modelKnown", "tokensUnknown", "costEstUnknown", "ttftUnknown", "durationKnown", "stopUnknown"], "KeyStrip/unknown"),
];
const rowTargets = [
  { spec: "ContentRow/shapeOnly", selector: "[data-sheet=rows] > .content-row:nth-child(1)" },
  { spec: "ContentRow/notRetained", selector: "[data-sheet=rows] > .content-row:nth-child(2)" },
  { spec: "ContentRow/toolCall", selector: "[data-sheet=rows] > .content-row:nth-child(3)" },
  { spec: "ContentRow/retainedWithBody", selector: "[data-sheet=retained] > .content-row" },
];
const headerTargets = [
  { spec: "HeaderList/rowHost", selector: ".header-row:nth-child(1)" },
  {
    spec: "HeaderList/rowAuthorizationRedacted",
    selector: ".header-row:nth-child(2)",
  },
  {
    spec: "HeaderList/rowXClaudeCodeSessionId",
    selector: ".header-row:nth-child(3)",
  },
  { spec: "HeaderList/rowRequestClass", selector: ".header-row:nth-child(4)" },
];

export function CompDrawerSheet() {
  return (
    <ParityTags targets={[...cellTargets, ...headerTargets, ...rowTargets]}>
      <div className="flex flex-col items-start gap-8 p-4">
        {headers.map((h) => (
          <Box key={h.st}>
            <div data-parity={`DrawerHeader/${h.st}`} className="contents">
              <DrawerHeader
                channel="model"
                method="POST"
                path="/v1/messages"
                status={h.status}
                kind={h.kind}
                actions={<CopyButton label="Copy as curl" text={() => ""} />}
                onPrev={noop}
                onNext={noop}
                onClose={noop}
              />
            </div>
            <div data-parity={`DrawerMeta/${h.st}`} className="contents">
              <DrawerMeta parts={h.meta} />
            </div>
          </Box>
        ))}

        <P spec="KeyStrip/values" track={822}>
          <KeyStrip cells={values} />
        </P>
        <P spec="KeyStrip/unknown" track={822}>
          <KeyStrip cells={unknown} />
        </P>

        <div className="flex gap-8">
          <P spec="DrawerTabs/default" track={434}>
            <DrawerTabs value="summary" onChange={noop} />
          </P>
          <P spec="DrawerTabs/noWireData" track={434}>
            <DrawerTabs value="summary" onChange={noop} raw="none" />
          </P>
        </div>

        <Box sheet="rows">
          <ContentRow row={{ label: "tool_result · Read", detail: "src/auth/session.ts · 132 lines", size: "4.1 KB" }} />
          <ContentRow row={{ label: "text", detail: "not retained", size: "212 chars" }} />
          <ContentRow row={{ label: "tool_use · Edit", detail: "src/auth/session.ts → ran 62 ms · +14 −3", size: "1.3 KB", tone: "tool" }} />
        </Box>
        <Box sheet="retained">
          <ContentRow
            row={{
              label: "tool_use · Edit",
              detail: "src/auth/session.ts → ran 62 ms · +14 −3",
              size: "1.3 KB",
              tone: "tool",
              body: "-   s.token = token\n+   if (await isExpired(s)) {\n+     s.token = await fetchToken(s.id)\n+   }",
            }}
          />
        </Box>

        <div className="flex flex-wrap items-start gap-8">
          <P spec="DetailSection/default" track={400}>
            <DetailSection
              className="border border-line bg-surface"
              title="Tokens & cost"
              accent="count"
              rows={[
                { k: "input · uncached", v: "412" },
                { k: "cache read", v: "42,611" },
                { k: "output", v: "1,204" },
                { k: "costUSD", v: "$0.21 · ✓ matches usage", tone: "ok" },
                { k: "speed", v: "— not sent", tone: "muted" },
              ]}
            />
          </P>
          <P spec="HeaderList/default" track={400}>
            <HeaderList
              className="border border-line bg-surface"
              headers={[
                { name: "host", value: "api.anthropic.com" },
                {
                  name: "authorization",
                  value: "[redacted · key_hash 9e2…]",
                  redacted: true,
                },
                { name: "x-claude-code-session-id", value: "4c1a…77e0" },
                { name: "request-class", value: "main" },
              ]}
            />
          </P>
          <P spec="StreamEventList/default" track={400}>
            <StreamEventList
              className="border border-line bg-surface px-4 py-2"
              events={[
                {
                  tOffsetMs: 1870,
                  type: "message_start",
                  detail: "id msg_01…b2",
                },
                {
                  tOffsetMs: 1900,
                  type: "content_block_delta",
                  count: 18,
                  detail: "thinking_delta",
                },
                {
                  tOffsetMs: 5300,
                  type: "content_block_start [2]",
                  detail: "tool_use · Edit",
                },
                { tOffsetMs: 9300, type: "message_stop", detail: "" },
              ]}
            />
          </P>
        </div>
        <P spec="TimingWaterfall/default" track={620}>
          <TimingWaterfall
            className="border border-line bg-surface"
            phases={[
              { label: "accept + TLS", startMs: null, durMs: null },
              { label: "upstream connect", startMs: null, durMs: null },
              { label: "first byte", startMs: 0, durMs: 1870 },
              { label: "streaming", startMs: 1870, durMs: 7430 },
              { label: "total", startMs: 0, durMs: 9300 },
            ]}
          />
        </P>
        <P spec="FieldList/default" track={562}>
          <FieldList
            className="border border-line bg-surface"
            fields={[
              { key: "session_id", value: "4c1a…77e0" },
              { key: "usage.output_tokens", value: "1,204" },
              { key: "usage.service_tier", value: "standard" },
              { key: "bytes_up / bytes_down", value: null },
            ]}
          />
        </P>
      </div>
    </ParityTags>
  );
}

const meta = {
  title: "Canvas/Drawer/DrawerHeader",
  parameters: { layout: "padded" },
  excludeStories: ["CompDrawerSheet"],
} satisfies Meta;
export default meta;
export const Canvas: StoryObj = { render: () => <CompDrawerSheet /> };
