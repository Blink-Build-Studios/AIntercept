import { describe, expect, it } from "vitest";
import one from "../api/samples/session_turns/one";
import subagents from "../api/samples/session_turns/subagents";
import withContent from "../api/samples/session_turns/with_content";
import codex from "../api/samples/session_turns/codex";
import work from "../api/samples/person_work/one";
import { workText } from "./work";
import { askedText, datedRows, factText, groupLine, outcomeFacts, repoView, toolFacts, turnView, typesText, type Turn } from "./turns";

const turn = one.data[0];
if (!turn) throw new Error("sample session_turns/one has no turn");

describe("groupLine (handoff §11R, CompTurns: title, then types or paths, steps, time)", () => {
  it("names each group by what its steps did", () => {
    expect(turn.groups.map((g) => [g.kind, groupLine(g).title])).toEqual([
      ["read", "Read 2 files"],
      ["edit", "Edited 1 file"],
      ["run", "Ran commands ×2"], // no program without content
      ["reply", "Reply"],
    ]);
  });

  it("puts file types (metadata) or paths (content) before steps and time", () => {
    const read = turn.groups[0];
    expect(read && groupLine(read).meta).toMatch(/^2 steps · /); // no file types without content
    const edit = { ...(turn.groups[1] as NonNullable<(typeof turn.groups)[1]>), paths: ["src/auth/cache.ts", "src/auth/refresh.ts"] };
    expect(groupLine(edit).meta).toBe("src/auth/cache.ts, src/auth/refresh.ts · 1 step");
  });

  it("names MCP servers, web use and spawns", () => {
    expect(subagents.data[0]?.groups.map((g) => groupLine(g).title)).toEqual(["MCP linear-server ×1", expect.stringMatching(/^(Searched|Fetched)/), "Spawned 1 subagent"]);
  });

  it("types read as `.ts ×5`, most first", () => {
    expect(typesText({ md: 1, ts: 5 })).toBe(".ts ×5, .md ×1");
    expect(typesText({})).toBeNull();
  });
});

describe("turnView", () => {
  it("numbers steps as the API does and keeps a command group's exit codes", () => {
    const v = turnView(turn);
    expect(v.groups[2]?.exits).toEqual([]); // exit codes need content
    expect(v.groups[0]?.steps.map((s) => s.label)).toEqual(["step 1", "step 2"]);
  });

  it("gives a recorded reply its preview, and none without content", () => {
    expect(turnView(withContent.data[0] ?? turn).groups.at(-1)?.reply?.text).toBe("Fixed the flaky test.");
    expect(turnView(turn).groups.at(-1)?.reply).toBeNull();
  });

  it("puts a subagent's steps before its work, and links a Codex thread to its session", () => {
    const sub = turnView(subagents.data[0] ?? turn).subagents[0];
    expect(sub?.name).toBe("test-writer");
    expect(workText(sub?.work ?? [])).toMatch(/^1 step/);
    expect(turnView(codex.data[0] ?? turn).subagents[0]?.sessionId).toBeTruthy();
  });

  it("asks with the prompt's size without content, its text with it", () => {
    expect(askedText(turn)).toEqual({ text: "—", recorded: false }); // its size needs content
    expect(askedText({ asked: { retained: true, text: "fix it", chars: 6, images: 0, pastedBytes: null } })).toEqual({ text: "“fix it”", recorded: true });
  });

  it("adds the prompt's images, either way (§11S: `212 chars · 1 image`)", () => {
    expect(askedText({ asked: { retained: false, text: null, chars: 212, images: 1, pastedBytes: null } }).text).toBe("212 chars · 1 image");
    expect(askedText({ asked: { retained: true, text: "see", chars: 3, images: 2, pastedBytes: null } }).text).toBe("“see” · 2 images");
  });
});

describe("repoView (the Work tab)", () => {
  it("groups sessions under their repo with each session's own work line", () => {
    const g = work.data[0];
    if (!g) throw new Error("sample person_work/one has no repo");
    const v = repoView(g);
    expect(v.rows).toHaveLength(g.rows.length);
    expect(v.sessions).toBe(g.sessions);
  });
});

describe("datedRows (TurnRow: the day on the first row and at each day change)", () => {
  it("dates the first row and each new day", () => {
    expect(datedRows(["2026-11-03T10:02:00", "2026-11-03T10:09:00", "2026-11-04T09:00:00", "2026-11-04T09:30:00"])).toEqual([true, false, true, false]);
  });
});

describe("outcomeFacts (handoff §11R, CompTurns OutcomeSlot: what we capture; zeros left out)", () => {
  const o = { commits: 2, headFrom: "a41f00", headTo: "9c0200", editedFiles: 23, editTypes: { ts: 17, md: 4, json: 2 }, lastTest: { program: "npm test", exitCode: 0, t: "2026-11-03T12:00:00Z" }, pushes: 0, prs: [2] };
  it("says commits on the branch and the head range, files edited by type, the last test, PRs", () => {
    expect(outcomeFacts(o, { branch: "partner-readiness" }).map(factText)).toEqual([
      "2 commits on partner-readiness (a41f → 9c02)",
      "23 files edited (.ts ×17, .md ×4, .json ×2)",
      "last npm test passed",
      "PR #2",
    ]);
  });
  it("leaves zeros out", () => {
    expect(outcomeFacts({ ...o, commits: 0, editedFiles: null, editTypes: {}, lastTest: null, prs: [], pushes: 1 }, {}).map(factText)).toEqual(["pushed"]);
  });
});

// Handoff §11S: without recorded content a turn says what the wire carries, tool names and counts.
describe("turnView without content (handoff §11S, CompTurns content-free)", () => {
  const step = (n: number) => ({ ...(turn.groups[0]?.steps[0] as NonNullable<(typeof turn.groups)[0]>["steps"][0]), step: n, requestId: `r${n}` });
  const free: Turn = {
    ...turn,
    live: false,
    asked: { ...turn.asked, retained: false, text: null },
    tools: { Read: 4, Bash: 3, Edit: 2, Task: 1 },
    groups: [
      { ...(turn.groups[0] as NonNullable<(typeof turn.groups)[0]>), kind: "read" as const, tools: { Read: 5 }, steps: [step(1), step(2), step(3), step(4)], durationMs: 41_000 },
      { ...(turn.groups[0] as NonNullable<(typeof turn.groups)[0]>), kind: "mcp" as const, server: "linear", tools: { mcp__linear__list_issues: 2, mcp__linear__get_issue: 1 }, steps: [step(5), step(6), step(7)], durationMs: null },
      { ...(turn.groups[0] as NonNullable<(typeof turn.groups)[0]>), kind: "think" as const, tools: {}, steps: [step(8), step(9)], durationMs: 100_000, tokensIn: 210_000, tokensOut: 3_200 },
    ],
    subagents: [{ agentId: "a1", name: null, sessionId: null, steps: 9, work: turn.work, tools: { Write: 1, Bash: 2 }, tokensIn: 180_000, tokensOut: 2_200, groups: [] }],
  };

  it("the work line is the turn's tools by name, most first", () => {
    expect(workText(turnView(free).work)).toBe("Read ×4 · Bash ×3 · Edit ×2 · 1 subagent");
    expect(workText(turnView({ ...free, live: true }).work)).toBe("Read ×4 · Bash ×3 · Edit ×2 · 1 subagent …");
    expect(workText(turnView({ ...free, tools: {} }).work)).toBe("no tools");
  });

  it("groups are titled by tool name; an MCP group lists its tools; no tool reads No tool", () => {
    expect(turnView(free).groups.map((g) => [g.title, g.meta])).toEqual([
      ["Read ×5", "4 steps · 41 s"],
      ["mcp:linear ×3", "list_issues ×2 · get_issue · 3 steps"],
      ["No tool", "2 steps · 1m 40s · 210k → 3.2k"],
    ]);
  });

  it("a subagent is `subagent N` with its steps and tools", () => {
    const sub = turnView(free).subagents[0];
    expect([sub?.name, sub?.n, workText(sub?.work ?? [])]).toEqual([null, 1, "9 steps · Bash ×2 · Write ×1"]);
  });
});

describe("toolFacts (handoff §11S, OutcomeSlot content-free)", () => {
  it("turns · steps · tool calls with the top tools, then subagents", () => {
    expect(toolFacts({ turns: 34, steps: 640, calls: 1212, byName: { Bash: 488, Read: 402, Edit: 171, Grep: 96, Glob: 31, Task: 6 } }).map(factText)).toEqual([
      "34 turns",
      "640 steps",
      "1,212 tool calls: Bash ×488 · Read ×402 · Edit ×171 · Grep ×96 · 6 subagents",
    ]);
    expect(toolFacts({ sessions: 11, turns: 201, calls: 0, byName: {} }).map(factText)).toEqual(["11 sessions", "201 turns"]);
  });
});
