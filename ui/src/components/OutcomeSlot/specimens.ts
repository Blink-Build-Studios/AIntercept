// CompTurns "OutcomeSlot": the sheet's own facts.
import type { OutcomeFact } from "./OutcomeFacts";

export const sessionFacts: OutcomeFact[] = [
  ["2 commits on ", { strong: "partner-readiness" }, " (a41f → 9c02)"],
  ["23 files edited (.ts ×17, .md ×4, .json ×2)"],
  ["last ", { strong: "npm test" }, " exited 0"],
  ["PR #2 open"],
];
export const personFacts: OutcomeFact[] = [["6 commits in ", { strong: "aintercept" }], ["41 files edited"], ["PR #2 open"], ["5 earlier sessions without a repo (pre-Nov 2)"]];
