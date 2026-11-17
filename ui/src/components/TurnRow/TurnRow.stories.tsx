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
  args: { onToggle: fn(), expanded: false, turn: TURN_SPECIMENS.default, work: ["Read ×31 · Bash ×6 · Grep ×2"] },
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

const openWork = ["read 5 · edited 2 files +31 −9 · ran npm test ×4: ", { failed: "3 failed" }, ", then passed"];

/** Content-free (§11S): the prompt's size, tool names and counts. */
export const Default: Story = { args: { dated: true } };
/** Expanded: raised, its work groups below (by tool name); a group opens to its steps. */
export const Open: Story = {
  args: {
    turn: TURN_SPECIMENS.open,
    work: ["Read ×5 · Edit ×2 · Bash ×4"],
    expanded: true,
    children: (
      <>
        <WorkGroup kind="read" title="Read ×5" meta="4 steps · 41 s" expanded={false} onToggle={fn()} />
        <WorkGroup kind="run" title="Bash ×4" meta="4 steps · 2m 12s" expanded onToggle={fn()}>
          <TurnStep label="step 22" detail="sonnet · 96k → 610 · 4.1 s · tool_use · Bash" onOpen={fn()} />
        </WorkGroup>
        <WorkGroup kind="edit" title="Edit ×2" meta="3 steps" expanded={false} onToggle={fn()} />
        <WorkGroup kind="mcp" title="mcp:linear ×3" meta="list_issues ×2 · get_issue · 3 steps" expanded={false} onToggle={fn()} />
        <WorkGroup kind="think" title="No tool" meta="22 steps · 1m 40s · 210k → 3.2k" expanded={false} onToggle={fn()} />
        <WorkGroup kind="reply" title="Reply" meta="1.2k tokens" expanded={false} onToggle={fn()} />
        <SubagentRow name={null} n={1} work={["9 steps · Write ×1 · Bash ×2"]} tokens="180k → 2.2k" onOpen={fn()} />
      </>
    ),
  },
};
export const InterruptedTurn: Story = { args: { turn: TURN_SPECIMENS.interrupted, work: ["Edit ×1"] } };
/** Failed calls (API errors, overloads, retries): `!`. */
export const FailedCalls: Story = { args: { turn: TURN_SPECIMENS.failedCalls, work: ["Read ×4 · Edit ×2 · Bash ×3 · 1 subagent"] } };
/** Still running: its elapsed time in --live. */
export const Live: Story = { args: { turn: TURN_SPECIMENS.live, work: ["Read ×3 · Bash ×1 …"], now: "2026-11-04T15:13:04" } };
/** Content recorded: the prompt's text, pastes inline. */
export const WithContent: Story = {
  args: {
    turn: TURN_SPECIMENS.withContent,
    work: openWork,
    expanded: true,
    children: (
      <>
        <WorkGroup kind="edit" title="Edited 2 files" meta="+31 −9 · src/auth/cache.ts, src/auth/refresh.ts" expanded={false} onToggle={fn()} />
        <WorkGroup kind="run" title="Ran npm test ×4" meta="4 steps · 2m 12s" exits={[1, 1, 1, 0]} expanded={false} onToggle={fn()} />
        <SubagentRow name="test-writer" n={1} work={["9 steps · wrote 1 file (.test.ts) +66 · ran npm test ×2: passed"]} tokens="180k → 2.2k" onOpen={fn()} />
        <WorkGroup kind="reply" title="Reply" meta="1.2k tokens" expanded onToggle={fn()}>
          <ReplyPreview text="Fixed: two refreshes could race and both write the cache. Now the second waits on the first’s promise…" size="1.2k tokens" onExpand={fn()} />
        </WorkGroup>
      </>
    ),
  },
};
/** Codex, content-free: its tool names as sent. */
export const Codex: Story = { args: { turn: TURN_SPECIMENS.codex, work: ["exec_command ×141 · apply_patch ×18 · mcp:linear ×9"], dated: true } };
export const Hover: Story = { args: { "data-force": "hover" } };
export const Focus: Story = { args: { "data-force": "focus" } };
/** The timeline points at this turn (§6B.5). */
export const Linked: Story = { args: { "data-force": "linked" } };
/** Landed on from a timeline block (§11H.6). */
export const Arrived: Story = { args: { arrived: true, expanded: true } };
