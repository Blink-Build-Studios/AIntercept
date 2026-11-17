import { describe, expect, it } from "vitest";
import one from "../api/samples/session_turns/one";
import subagents from "../api/samples/session_turns/subagents";
import withContent from "../api/samples/session_turns/with_content";
import codex from "../api/samples/session_turns/codex";
import work from "../api/samples/person_work/one";
import { workText } from "./work";
import { askedText, datedRows, factText, groupLine, outcomeFacts, repoView, turnView, typesText } from "./turns";

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

  it("names other tools, web use and spawns", () => {
    expect(subagents.data[0]?.groups.map((g) => groupLine(g).title)).toEqual(["Used 1 tool", expect.stringMatching(/^(Searched|Fetched)/), "Spawned 1 subagent"]);
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
      "last npm test exited 0",
      "PR #2",
    ]);
  });
  it("leaves zeros out", () => {
    expect(outcomeFacts({ ...o, commits: 0, editedFiles: null, editTypes: {}, lastTest: null, prs: [], pushes: 1 }, {}).map(factText)).toEqual(["pushed"]);
  });
});
