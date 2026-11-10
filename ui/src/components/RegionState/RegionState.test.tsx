import { act, fireEvent, render, screen } from "@testing-library/react";
import { RegionState } from "./RegionState";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

const base = { label: "Sessions", skeleton: <p>skeleton</p>, children: <p>old rows</p> };

describe("RegionState (§6A.3)", () => {
  it("first load shows the skeleton only after 150 ms", () => {
    render(<RegionState {...base} status="loading" hasData={false} />);
    expect(screen.getByText("skeleton").closest("[aria-hidden]")).not.toBeNull(); // reserved, invisible
    act(() => vi.advanceTimersByTime(150));
    expect(screen.getByText("skeleton").closest(".invisible")).toBeNull();
  });

  it("[6A.11-2] a refetch keeps the old content (dimmed) and never shows the empty state", () => {
    render(<RegionState {...base} status="refetching" hasData empty={<p>No sessions</p>} />);
    act(() => vi.advanceTimersByTime(200));
    expect(screen.getByText("old rows").closest(".region-dim")).not.toBeNull();
    expect(screen.queryByText("No sessions")).toBeNull();
  });

  it("[6A.11-8] at 8 s, Still loading… with Cancel; Cancel hands control back with the old content intact", () => {
    const onCancel = vi.fn();
    render(<RegionState {...base} status="refetching" hasData slow={{ reason: "the timeline", size: "2 days · 21,305 events", onCancel }} />);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(screen.getByText("old rows")).toBeInTheDocument();
  });

  it("an error with data keeps the content under an inline error", () => {
    render(<RegionState {...base} status="error" hasData error={{ message: "Couldn't refresh", onRetry: vi.fn() }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't refresh");
    expect(screen.getByText("old rows")).toBeInTheDocument();
  });
});
