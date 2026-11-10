import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createLinkedHoverStore, LinkedHoverProvider } from "../../lib/linkedHover";
import { TooltipProvider } from "../Tooltip/Tooltip";
import { SessionRow, type Session } from "./SessionRow";

const session: Session = {
  agent: "claude-code",
  client: "cli",
  clientVersion: null,
  origin: null,
  children: null,
  hiddenCalls: 0,
  commit: null,
  sessionId: "s-1",
  start: "2026-10-23T17:00:00",
  end: "2026-10-23T20:30:00",
  live: false,
  repo: null,
  branch: null,
  label: { source: "recap", text: "Added CSV export; tests pass" },
  capturedFromPrompt: null,
  prompts: 8,
  modelCalls: 282,
  events: null,
  friction: { frustrated: 0, interrupted: 0, destructive: 0 },
  bypassPct: 0,
  coverage: { captured: 282, notCaptured: 0, reported: 282, windows: [] },
  fieldsMissing: false,
};

const renderRow = () => {
  const store = createLinkedHoverStore();
  render(
    <TooltipProvider>
      <LinkedHoverProvider store={store}>
        <div style={{ height: 40, overflow: "auto" }} data-testid="scroller">
          <SessionRow session={session} href="#" now="2026-11-04T10:00:00" />
        </div>
      </LinkedHoverProvider>
    </TooltipProvider>,
  );
  return store;
};

describe("SessionRow linked hover (handoff §6B.5)", () => {
  it("[6B.11-16] Hovering a SessionRow points the timeline at its band; the band hovered marks the row; neither scrolls", () => {
    const store = renderRow();
    const row = screen.getByRole("row");
    const scroller = screen.getByTestId("scroller");
    const top = scroller.scrollTop;
    fireEvent.pointerEnter(row);
    expect(store.get()).toEqual({ source: "table", ids: ["s-1"], span: [session.start, session.end] });
    fireEvent.pointerLeave(row);
    expect(store.get()).toBeNull();
    act(() => store.set({ source: "timeline", ids: ["s-1"], span: [session.start, session.end as string] }));
    expect(row).toHaveAttribute("data-linked", "true");
    // A bin span alone doesn't mark a session row; only its own band does.
    act(() => store.set({ source: "timeline", span: [session.start, session.end as string] }));
    expect(row).not.toHaveAttribute("data-linked");
    expect(scroller.scrollTop).toBe(top);
  });

  it("[6B.11-17] A null field renders — and its tooltip says why; never 0", () => {
    vi.useFakeTimers();
    renderRow();
    const events = screen.getByLabelText("unknown: no analytics captured");
    expect(events).toHaveTextContent("—");
    expect(events).not.toHaveTextContent("0");
    fireEvent.pointerEnter(events);
    act(() => void vi.advanceTimersByTime(400));
    expect(screen.getByRole("tooltip")).toHaveTextContent(/Events\s*—\s+no analytics captured/);
    vi.useRealTimers();
  });
});
