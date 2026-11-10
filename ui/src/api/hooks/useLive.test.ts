import { describe, expect, it } from "vitest";
import { delayedSince, failureStreak, pollInterval } from "./useLive";

const q = (status: "success" | "error", errors = 0) => ({ state: { status, errorUpdateCount: errors } });

describe("live polling (handoff §5.3, BUILD_PLAN §2)", () => {
  it("polls 15 s visible, 30 s hidden, and not at all when paused", () => {
    expect(pollInterval(q("success"), false, true)).toBe(15_000);
    expect(pollInterval(q("success"), false, false)).toBe(30_000);
    expect(pollInterval(q("success"), true, true)).toBe(false);
  });
  it("backs off 5 s → 10 s → 30 s after failed polls, one query over time", () => {
    // TanStack restarts fetchFailureCount at every fetch; only errorUpdateCount grows.
    const query = q("success");
    const poll = (status: "success" | "error", errors: number) => {
      query.state = { status, errorUpdateCount: errors };
      return pollInterval(query, false, true);
    };
    expect(poll("success", 0)).toBe(15_000);
    expect([1, 2, 3, 4].map((n) => poll("error", n))).toEqual([5_000, 10_000, 30_000, 30_000]);
    expect(poll("error", 4)).toBe(30_000); // asked again, no new failure
    expect(poll("success", 4)).toBe(15_000);
    expect(poll("error", 5)).toBe(5_000); // a success ends the streak
  });
  it("counts a streak per query", () => {
    const a = {};
    const b = {};
    expect(failureStreak(a, { status: "error", errorUpdateCount: 1 })).toBe(1);
    expect(failureStreak(a, { status: "error", errorUpdateCount: 2 })).toBe(2);
    expect(failureStreak(b, { status: "error", errorUpdateCount: 7 })).toBe(1);
  });
  it("is delayed only while live and after 3 min without data", () => {
    const meta = { asOf: "2026-11-04T18:05:00Z", lastDataAt: "2026-11-04T18:00:00Z" };
    expect(delayedSince(meta, true)).toBe(meta.lastDataAt);
    expect(delayedSince(meta, false)).toBeUndefined();
    expect(delayedSince({ ...meta, lastDataAt: "2026-11-04T18:04:00Z" }, true)).toBeUndefined();
  });
});
