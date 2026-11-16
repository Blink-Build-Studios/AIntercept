import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { DataTable } from "../DataTable/DataTable";
import { TURN_SPECIMENS } from "./CompTurns.canvas.stories";
import { ReplyPreview } from "./ReplyPreview";
import { SubagentRow } from "./SubagentRow";
import { TURN_COLUMNS, TURN_MIN_WIDTH, TurnRow } from "./TurnRow";
import { TurnStep } from "./TurnStep";
import { WorkGroup } from "./WorkGroup";

const meta = {
  title: "Components/Turns/TurnRow",
  component: TurnRow,
  parameters: { layout: "padded" },
  args: { onToggle: fn(), expanded: false, turn: TURN_SPECIMENS.default, work: ["read 31 files · ran 6 commands (rg ×4, git ×2)"] },
  decorators: [
    (S) => (
      <DataTable expandable columns={TURN_COLUMNS} minWidth={TURN_MIN_WIDTH} label="Turns">
        <S />
      </DataTable>
    ),
  ],
} satisfies Meta<typeof TurnRow>;
export default meta;
type Story = StoryObj<typeof meta>;

const openWork = ["read 5 files · edited 2 files +31 −9 · ran npm test ×4: ", { failed: "3 failed" }, ", then passed"];

export const Default: Story = { args: { dated: true } };
/** Expanded: raised, its work groups below; a group opens to its steps. */
export const Open: Story = {
  args: {
    turn: TURN_SPECIMENS.open,
    work: openWork,
    expanded: true,
    children: (
      <>
        <WorkGroup kind="read" title="Read 5 files" meta=".ts ×5 · 4 steps · 41 s" expanded={false} onToggle={fn()} />
        <WorkGroup kind="run" title="Ran npm test ×4" meta="4 steps · 2m 12s" exits={[1, 1, 1, 0]} expanded onToggle={fn()}>
          <TurnStep label="step 22" detail="sonnet · 96k → 610 · 4.1 s · Bash npm · exit 1 · 18 s" onOpen={fn()} />
        </WorkGroup>
        <WorkGroup kind="edit" title="Edited 2 files" meta="+31 −9 · .ts ×2 · 3 steps" expanded={false} onToggle={fn()} />
        <WorkGroup kind="think" title="Thinking" meta="22 steps with no tool · 1m 40s · 210k → 3.2k" expanded={false} onToggle={fn()} />
        <WorkGroup kind="reply" title="Reply" meta="1.2k tokens" expanded={false} onToggle={fn()} />
        <SubagentRow name="test-writer" work={["9 steps · edited 1 file +66 · ran npm test ×2: passed"]} tokens="180k → 2.2k" onOpen={fn()} />
      </>
    ),
  },
};
export const InterruptedTurn: Story = { args: { turn: TURN_SPECIMENS.interrupted, work: ["edited 1 file +4 −0"] } };
/** Still running: its elapsed time in --live. */
export const Live: Story = { args: { turn: TURN_SPECIMENS.live, work: ["read 3 files · ran npm test ×1"], now: "2026-11-04T15:13:04" } };
/** Content recorded: the prompt's text, pastes inline. */
export const WithContent: Story = {
  args: {
    turn: TURN_SPECIMENS.withContent,
    work: openWork,
    expanded: true,
    children: (
      <>
        <WorkGroup kind="edit" title="Edited 2 files" meta="+31 −9 · src/auth/cache.ts, src/auth/refresh.ts" expanded={false} onToggle={fn()} />
        <WorkGroup kind="reply" title="Reply" meta="1.2k tokens" expanded onToggle={fn()}>
          <ReplyPreview text="Fixed: two refreshes could race and both write the cache. Now the second waits on the first’s promise…" size="1.2k tokens" onExpand={fn()} />
        </WorkGroup>
      </>
    ),
  },
};
/** Codex: no cost, tool counts only. */
export const Codex: Story = { args: { turn: TURN_SPECIMENS.codex, work: ["ran 141 commands · edited 18 files · used linear ×9"], dated: true } };
export const Hover: Story = { args: { "data-force": "hover" } };
export const Focus: Story = { args: { "data-force": "focus" } };
/** The timeline points at this turn (§6B.5). */
export const Linked: Story = { args: { "data-force": "linked" } };
/** Landed on from a timeline block (§11H.6). */
export const Arrived: Story = { args: { arrived: true, expanded: true } };
