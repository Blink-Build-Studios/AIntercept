import { describe, expect, it } from "vitest";
import { pickBin } from "./bins";

const H = 3_600_000;
const D = 24 * H;

describe("bin choice (handoff §11G.A5)", () => {
  it("picks the bin nearest 4px on a 1212px track, as the handoff's examples", () => {
    expect(pickBin(24 * H, 1212)).toBe(300);
    expect(pickBin(7 * D, 1212)).toBe(3600);
    expect(pickBin(14 * D, 1212)).toBe(3600);
    expect(pickBin(30 * D, 1212)).toBe(10_800);
    expect(pickBin(6.5 * D, 1212)).toBe(3600);
    expect(pickBin(2 * D, 1212)).toBe(900);
  });
  it("never goes under 2px", () => {
    expect(pickBin(400 * D, 1212)).toBe(86_400);
  });
});
