import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TYPING_MS, useDebounced } from "./useDebounced";

describe("useDebounced (text filters query once typing pauses)", () => {
  beforeEach(() => void vi.useFakeTimers());
  afterEach(() => void vi.useRealTimers());

  it("holds the old value while it keeps changing, then takes the last one", () => {
    const { result, rerender } = renderHook(({ v }) => useDebounced(v, TYPING_MS), { initialProps: { v: "" } });
    for (const v of ["r", "re", "req"]) {
      rerender({ v });
      act(() => void vi.advanceTimersByTime(TYPING_MS - 50));
      expect(result.current).toBe("");
    }
    act(() => void vi.advanceTimersByTime(60));
    expect(result.current).toBe("req");
  });
});
