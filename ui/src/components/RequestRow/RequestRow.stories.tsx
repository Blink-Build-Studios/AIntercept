import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import account from "../../api/samples/person_requests/account_calls";
import demo from "../../api/samples/person_requests/demo";
import missed from "../../api/samples/person_requests/not_captured";
import { DataTable } from "../DataTable/DataTable";
import { RequestRow, requestColumns, requestMinWidth, type Request } from "./RequestRow";
import { codexRequests, codexRequestsNoBodies, personRequests } from "../ProviderMark/providerSpecimens";

const rows: Request[] = [...demo.data, ...account.data, ...missed.data];
const byKind = (k: Request["kind"], extra: (r: Request) => boolean = () => true) =>
  rows.find((r) => r.kind === k && r.captured && extra(r)) ?? rows[0]!;

const meta = {
  title: "Components/Rows/RequestRow",
  component: RequestRow,
  parameters: { layout: "padded" },
  args: { row: byKind("model", (r) => !r.background), showSession: true, onOpen: fn() },
  decorators: [
    (Story, { args }) => (
      <DataTable columns={requestColumns(true, !!args.agentDot)} minWidth={requestMinWidth(true, !!args.agentDot)} label="Requests">
        {Story()}
      </DataTable>
    ),
  ],
} satisfies Meta<typeof RequestRow>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Model: Story = {};
export const Background: Story = { args: { row: byKind("analytics") } };
export const Flags: Story = { args: { row: byKind("flags") } };
export const Count: Story = { args: { row: byKind("count") } };
export const Account: Story = { args: { row: byKind("account") } };
export const Error: Story = { args: { row: { ...byKind("model"), status: 529, durationMs: 612 } } };
export const NotCaptured: Story = { args: { row: missed.data[0]! } };
export const Selected: Story = { args: { selected: true } };
/** Arrived since the last look: starts on surface-raised and fades (held here on the first frame). */
export const New: Story = { args: { "data-force": "new" } };
export const Hover: Story = { args: { "data-force": "hover" } };
/** The timeline points at this request (a bin or its instant hovered): hover fill and an ink-muted rule (§6B.5). */
export const Linked: Story = { args: { "data-force": "linked" } };
/** Keyboard focus in the table's roving group: a 2px focus ring inset (§6B.8). */
export const Focus: Story = { args: { "data-force": "focus" } };
/** Person scope: the session cell is its own link to the session page (§6B.6). */
export const InnerTargetHover: Story = { args: { sessionHref: (id: string) => `#/sessions/${id}`, "data-force": "innerHover" } };

const codex = (spec: string) => codexRequests.find((r) => r.spec === spec) ?? codexRequests[0]!;
/** A Codex model call (§11L.6): `#1 · step 37` bold · model and effort · tokens · what it produced · stop. */
export const CodexModelCall: Story = { args: { row: codex("RequestRow/codexModelCall").row, detail: codex("RequestRow/codexModelCall").detail, showSession: false } };
/** A model call that wrote text: its first words, quoted. */
export const CodexModelCallText: Story = { args: { row: codex("RequestRow/codexModelCallText").row, detail: codex("RequestRow/codexModelCallText").detail, showSession: false } };
/** An analytics event: its name and one key value. */
export const CodexAnalytics: Story = { args: { row: codex("RequestRow/codexAnalytics").row, channel: "analytics", detail: codex("RequestRow/codexAnalytics").detail, showSession: false } };
/** Usage & account: the endpoint and its key value; the usual path is dropped. */
export const CodexUsage: Story = { args: { row: codex("RequestRow/codexUsage").row, channel: "usage", detail: codex("RequestRow/codexUsage").detail, showSession: false } };
/** A hidden prewarm call, shown: a quiet row. */
export const CodexPrewarmShown: Story = { args: { row: codex("RequestRow/codexPrewarmShown").row, detail: codex("RequestRow/codexPrewarmShown").detail, showSession: false } };

const noBodies = (spec: string) => codexRequestsNoBodies.find((r) => r.spec === spec) ?? codexRequestsNoBodies[0]!;
/** Without recorded bodies (§11M 3.1): tool counts stay. */
export const CodexModelCallNoBodies: Story = { args: { row: noBodies("RequestRow/codexModelCallNoBodies").row, detail: noBodies("RequestRow/codexModelCallNoBodies").detail, showSession: false } };
/** Without recorded bodies, a text-only call reads `text`. */
export const CodexModelCallTextNoBodies: Story = {
  args: { row: noBodies("RequestRow/codexModelCallTextNoBodies").row, detail: noBodies("RequestRow/codexModelCallTextNoBodies").detail, showSession: false },
};
const person = (spec: string) => personRequests.find((r) => r.spec === spec) ?? personRequests[0]!;
const personArgs = (spec: string) => ({ row: person(spec).row, detail: person(spec).detail, channel: person(spec).channel, agentDot: true, sessionHref: (id: string) => `#/sessions/${id}` });
/** A person's All requests in All agents (§11M 3.3): the agent dot, Detail per product. */
export const PersonClaudeCodeModel: Story = { args: personArgs("RequestRow/personClaudeCodeModel") };
export const PersonCodexModel: Story = { args: personArgs("RequestRow/personCodexModel") };
/** Codex metrics under the shared `other` channel. */
export const PersonCodexMetrics: Story = { args: personArgs("RequestRow/personCodexMetrics") };
export const PersonClaudeCodeAnalytics: Story = { args: personArgs("RequestRow/personClaudeCodeAnalytics") };
