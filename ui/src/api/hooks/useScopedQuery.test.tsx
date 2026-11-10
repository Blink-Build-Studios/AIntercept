import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { useScopedQuery } from "./useScopedQuery";

type Res = { window: string };

function View({ win }: { win: string }) {
  const q = useScopedQuery<Res>(["summary", win], `/summary?win=${win}`);
  return <p data-testid="shown">{q.data?.window ?? "none"}</p>;
}

describe("useScopedQuery", () => {
  it("[6A.11-5] two quick window changes: only the second result renders, even if the first resolves later", async () => {
    vi.useFakeTimers();
    const delays: Record<string, number> = { "1h": 10, "24h": 500, "7d": 50 }; // 24h is slow and resolves last
    const aborted: string[] = [];
    vi.stubGlobal("fetch", (url: string, init: RequestInit) => {
      const win = new URL(url, "http://x").searchParams.get("win") ?? "";
      return new Promise<Response>((resolve, reject) => {
        const t = setTimeout(() => resolve(new Response(JSON.stringify({ window: win }), { status: 200 })), delays[win]);
        init.signal?.addEventListener("abort", () => {
          clearTimeout(t);
          aborted.push(win);
          reject(new DOMException("Aborted", "AbortError"));
        });
      });
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    const { rerender } = render(<View win="1h" />, { wrapper: wrap });
    await act(() => vi.advanceTimersByTimeAsync(20));
    expect(screen.getByTestId("shown")).toHaveTextContent("1h");

    rerender(<View win="24h" />); // slow request starts…
    rerender(<View win="7d" />); // …and is superseded immediately
    await act(() => vi.advanceTimersByTimeAsync(60));
    expect(screen.getByTestId("shown")).toHaveTextContent("7d");
    await act(() => vi.advanceTimersByTimeAsync(600)); // the 24h response would have landed now
    expect(screen.getByTestId("shown")).toHaveTextContent("7d");
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });
});
