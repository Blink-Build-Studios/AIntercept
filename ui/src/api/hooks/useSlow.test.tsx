import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SLOW_MS, useSlow } from "./useSlow";

describe("useSlow (§6A.1: 'Still loading…' with Cancel at 8 s)", () => {
  beforeEach(() => void vi.useFakeTimers());
  afterEach(() => void vi.useRealTimers());

  it("turns on after SLOW_MS of fetching, off when it stops; Cancel cancels the region's queries", () => {
    const client = new QueryClient();
    const cancel = vi.spyOn(client, "cancelQueries").mockResolvedValue();
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    const { result, rerender } = renderHook(({ fetching }) => useSlow(fetching, ["person", "p1", "requests"]), { initialProps: { fetching: true }, wrapper });
    act(() => void vi.advanceTimersByTime(SLOW_MS - 1));
    expect(result.current).toBeUndefined();
    act(() => void vi.advanceTimersByTime(1));
    expect(result.current).toBeDefined();
    result.current?.onCancel();
    expect(cancel).toHaveBeenCalledWith({ queryKey: ["person", "p1", "requests"], exact: false });
    rerender({ fetching: false });
    expect(result.current).toBeUndefined();
  });
});
