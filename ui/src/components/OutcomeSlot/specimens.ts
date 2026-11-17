// CompTurns "OutcomeSlot" (handoff §11S): the sheet's own facts, content-free and with content.
import type { OutcomeFact } from "./OutcomeFacts";

/** Content-free: turns · steps · tool calls with the top tools, then subagents. */
export const sessionFacts: OutcomeFact[] = [["34 turns"], ["640 steps"], ["1,212 tool calls: Bash ×488 · Read ×402 · Edit ×171 · Grep ×96 · 6 subagents"]];
/** With content: commits and head range, files edited by type, the last test outcome, PR. */
export const sessionFactsWithContent: OutcomeFact[] = [
  ["2 commits on ", { strong: "partner-readiness" }, " (a41f → 9c02)"],
  ["23 files edited (.ts ×17, .md ×4, .json ×2)"],
  ["last ", { strong: "npm test" }, " passed"],
  ["PR #2 open"],
];
export const personFacts: OutcomeFact[] = [["11 sessions"], ["201 turns"], ["4,980 tool calls: Bash ×2,104 · Read ×1,630 · Edit ×712"]];
export const personFactsWithContent: OutcomeFact[] = [["6 commits in ", { strong: "aintercept" }], ["41 files edited"], ["PR #2 open"]];
