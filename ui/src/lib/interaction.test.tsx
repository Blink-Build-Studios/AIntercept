import { act, renderHook } from "@testing-library/react";
import { useDelayedFlag, useSlowFlag } from "./interaction";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("useDelayedFlag (§6A.1)", () => {
  it("[6A.11-3] a response under 150 ms never shows an indicator", () => {
    const { result, rerender } = renderHook(({ p }) => useDelayedFlag(p), { initialProps: { p: true } });
    act(() => vi.advanceTimersByTime(100));
    expect(result.current).toBe(false);
    rerender({ p: false });
    act(() => vi.advanceTimersByTime(1000));
    expect(result.current).toBe(false);
  });

  it("[6A.11-4] a 200 ms response shows the indicator for at least 400 ms", () => {
    const { result, rerender } = renderHook(({ p }) => useDelayedFlag(p), { initialProps: { p: true } });
    act(() => vi.advanceTimersByTime(150));
    expect(result.current).toBe(true); // shown at 150 ms
    act(() => vi.advanceTimersByTime(50));
    rerender({ p: false }); // settled at 200 ms
    act(() => vi.advanceTimersByTime(349));
    expect(result.current).toBe(true); // still visible at 549 ms (< 150 + 400)
    act(() => vi.advanceTimersByTime(1));
    expect(result.current).toBe(false);
  });
});

describe("useSlowFlag", () => {
  it("turns on at 8 s and off when settled", () => {
    const { result, rerender } = renderHook(({ p }) => useSlowFlag(p), { initialProps: { p: true } });
    act(() => vi.advanceTimersByTime(7999));
    expect(result.current).toBe(false);
    act(() => vi.advanceTimersByTime(1));
    expect(result.current).toBe(true);
    rerender({ p: false });
    expect(result.current).toBe(false);
  });
});
