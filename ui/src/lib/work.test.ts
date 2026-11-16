import { describe, expect, it } from "vitest";
import { EMPTY_WORK, workLine, workText, type Work } from "./work";

const w = (over: Partial<Work>): Work => ({ ...EMPTY_WORK, ...over });
const line = (over: Partial<Work>) => workText(workLine(w(over)));

// CompTurns → "Work line vocabulary": one case per phrase, then the ordering rules.
describe("workLine (handoff §11R, CompTurns vocabulary)", () => {
  it("read N files: the distinct files where named, else the calls", () => {
    expect(line({ reads: 31 })).toBe("read 31 files");
    expect(line({ reads: 1 })).toBe("read 1 file");
    expect(line({ reads: 9, readFiles: 5 })).toBe("read 5 files");
  });

  it("edited N files +a −b; without line counts, the files alone", () => {
    expect(line({ edits: 2, linesAdded: 31, linesRemoved: 9 })).toBe("edited 2 files +31 −9");
    expect(line({ edits: 1, linesAdded: 4, linesRemoved: 0 })).toBe("edited 1 file +4 −0");
    expect(line({ edits: 18 })).toBe("edited 18 files");
    expect(line({ edits: 6, editedFiles: 2, linesAdded: 31, linesRemoved: 9 })).toBe("edited 2 files +31 −9");
  });

  it("ran <program> ×N: k failed, then passed (one program; failed is its own part, never red)", () => {
    const parts = workLine(w({ commands: [{ program: "npm test", runs: 4, failed: 3, lastExit: 0 }] }));
    expect(workText(parts)).toBe("ran npm test ×4: 3 failed, then passed");
    expect(parts).toContainEqual({ failed: "3 failed" });
    expect(line({ commands: [{ program: "npm test", runs: 2, failed: 0, lastExit: 0 }] })).toBe("ran npm test ×2: passed");
    expect(line({ commands: [{ program: "npm test", runs: 3, failed: 1, lastExit: 1 }] })).toBe("ran npm test ×3: 1 failed");
    expect(line({ commands: [{ program: "git", runs: 2, failed: 0, lastExit: null }] })).toBe("ran git ×2");
  });

  it("ran N commands (p ×a, q ×b) for several programs", () => {
    expect(
      line({
        commands: [
          { program: "rg", runs: 4, failed: 0, lastExit: null },
          { program: "git", runs: 2, failed: 0, lastExit: null },
        ],
      }),
    ).toBe("ran 6 commands (rg ×4, git ×2)");
  });

  it("ran N commands when the agent sends no program (Codex without content)", () => {
    expect(line({ commands: [{ program: null, runs: 141, failed: 0, lastExit: null }] })).toBe("ran 141 commands");
  });

  it("spawned N subagents", () => {
    expect(line({ subagents: [{ name: "test-writer", count: 1 }] })).toBe("spawned 1 subagent");
    expect(line({ subagents: [{ name: null, count: 2 }, { name: "Explore", count: 1 }] })).toBe("spawned 3 subagents");
  });

  it("searched N · fetched N pages", () => {
    expect(line({ web: { searches: 2, fetches: 3 } })).toBe("searched 2 · fetched 3 pages");
    expect(line({ web: { searches: 0, fetches: 1 } })).toBe("fetched 1 page");
  });

  it("used <server> ×N", () => {
    expect(line({ mcp: [{ server: "playwright", calls: 9 }] })).toBe("used playwright ×9");
    expect(line({ mcp: [{ server: "", calls: 1 }] })).toBe("used MCP ×1");
  });

  it("N commits · pushed · PR #n", () => {
    expect(line({ commits: 2, pushes: 1, prs: [2] })).toBe("2 commits · pushed · PR #2");
    expect(line({ commits: 1 })).toBe("1 commit");
  });

  it("orders reads, edits, commands, subagents, web, MCP, git, and leaves zero kinds out", () => {
    expect(
      line({
        commits: 1,
        mcp: [{ server: "linear", calls: 1 }],
        web: { searches: 1, fetches: 0 },
        subagents: [{ name: null, count: 1 }],
        commands: [{ program: "npm test", runs: 4, failed: 3, lastExit: 0 }],
        edits: 2,
        linesAdded: 31,
        linesRemoved: 9,
        reads: 5,
      }),
    ).toBe("read 5 files · edited 2 files +31 −9 · ran npm test ×4: 3 failed, then passed · spawned 1 subagent · searched 1 · used linear ×1 · 1 commit");
    expect(workLine(EMPTY_WORK)).toEqual([]);
  });
});

