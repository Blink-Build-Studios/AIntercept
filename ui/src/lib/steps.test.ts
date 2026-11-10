import { describe, expect, it } from "vitest";
import type { StepData } from "../api/types";
import { longTurnSteps } from "../components/ProviderMark/providerSpecimens";
import { runParts, stepProduced, stepsShown, stripKind, turnRows } from "./steps";

const base = longTurnSteps.first.step;
const step = (i: number, over: Partial<StepData> = {}): StepData => ({
  ...base,
  requestId: `s${i}`,
  t: new Date(Date.parse(base.t) + i * 10_000).toISOString(),
  durationMs: 5_000,
  ...over,
});

describe("turnRows (handoff §11L.7)", () => {
  it("collapses 5 or more command-only steps into a run", () => {
    const steps = [...Array.from({ length: 6 }, (_, i) => step(i)), step(6, { does: "patch", patches: [{ file: "a.ts", added: 1, removed: 0 }] }), step(7)];
    const rows = turnRows(steps);
    expect(rows.map((r) => r.kind)).toEqual(["run", "step", "step"]);
    expect(rows[0]).toMatchObject({ first: 1, last: 6 });
    expect(stepsShown(rows, 2)).toBe(7);
  });

  it("leaves shorter stretches as numbered steps", () => {
    const steps = [step(0), step(1), step(2, { does: "text", text: "done" }), step(3)];
    expect(turnRows(steps).map((r) => (r.kind === "step" ? r.index : -1))).toEqual([1, 2, 3, 4]);
  });

  it("a text step breaks a run even when it also ran commands", () => {
    const steps = [...Array.from({ length: 4 }, (_, i) => step(i)), step(4, { text: "hm" }), ...Array.from({ length: 4 }, (_, i) => step(5 + i))];
    expect(turnRows(steps).every((r) => r.kind === "step")).toBe(true);
  });
});

describe("runParts", () => {
  it("degrades to counts, span and tokens without commands", () => {
    const parts = runParts(Array.from({ length: 5 }, (_, i) => step(i)));
    expect(parts).toHaveLength(1);
    expect(parts[0]).toMatch(/^exec ×5 · 45 s · /);
  });

  it("names notable commands and puts failures apart", () => {
    const run = Array.from({ length: 5 }, (_, i) => step(i, { commands: ["npm test"], exitCodes: [i % 2] }));
    expect(runParts(run)).toEqual(["exec ×5 (npm test ×5, ", { failed: "exit 1 ×2" }, expect.stringMatching(/^\) · 45 s · /)]);
  });
});

describe("step kinds", () => {
  it("maps what a step did to the strip's colours", () => {
    expect(stripKind(step(0, { does: "spawn" }))).toBe("exec");
    expect(stripKind(step(0, { does: "interrupt" }))).toBe("text");
    expect(stripKind(step(0, { does: "mcp" }))).toBe("mcp");
  });

  it("says what a numbered step produced", () => {
    expect(stepProduced(step(0, { does: "patch", patches: [{ file: "src/timer.ts", added: 12, removed: 3 }] }))).toBe("apply_patch src/timer.ts +12 −3");
    // Without recorded bodies a text-only step reads `text`, then its stop reason (§11M 3.1).
    expect(stepProduced(step(0, { does: "text", text: null, stopReason: "end_turn" }))).toBe("text · end_turn");
  });
});
