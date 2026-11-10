import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "../Tooltip/Tooltip";
import { BarTrack } from "./BarTrack";
import { Lane } from "./Lane";
import { Timeline } from "./Timeline";

const from = "2026-11-03T08:00:00Z";
const to = "2026-11-03T12:00:00Z";
const HOUR = 3600;

function Bins({ onSelect }: { onSelect: (r: { from: string; to: string } | null) => void }) {
  return (
    <TooltipProvider>
      <Timeline from={from} to={to} label="Activity" onSelect={onSelect}>
        <Lane label="Model calls" height={60} summary="model calls">
          <BarTrack
            bins={[{ t: "2026-11-03T09:00:00Z", v: 3 }]}
            binSeconds={HOUR}
            color="model"
            interact={{ tip: () => ({ body: "bin" }), label: (s) => `bin ${s.from}`, onSelect: (s) => onSelect({ from: s.from, to: s.to }) }}
          />
        </Lane>
      </Timeline>
    </TooltipProvider>
  );
}

describe("Timeline marks (handoff §6B.4, §6B.8)", () => {
  it("[6B.11-15] Clicking a bin sets the range to that bin; Enter on a focused bin does the same", () => {
    const onSelect = vi.fn();
    render(<Bins onSelect={onSelect} />);
    fireEvent.click(screen.getByRole("button", { name: "bin 2026-11-03T09:00:00.000Z" }));
    expect(onSelect).toHaveBeenLastCalledWith({ from: "2026-11-03T09:00:00.000Z", to: "2026-11-03T10:00:00.000Z" });

    const bin = screen.getByRole("button", { name: "bin 2026-11-03T10:00:00.000Z" });
    act(() => bin.focus());
    fireEvent.keyDown(bin, { key: "Enter" });
    expect(onSelect).toHaveBeenLastCalledWith({ from: "2026-11-03T10:00:00.000Z", to: "2026-11-03T11:00:00.000Z" });
  });

  it("The timeline is one tab stop; ← / → move between bins", () => {
    render(<Bins onSelect={vi.fn()} />);
    const bins = screen.getAllByRole("button", { name: /^bin / });
    expect(bins.filter((b) => b.tabIndex === 0)).toHaveLength(1);
    const first = bins[0];
    if (!first) throw new Error("no bins");
    act(() => first.focus());
    fireEvent.keyDown(first, { key: "ArrowRight" });
    expect(bins[1]).toHaveFocus();
    fireEvent.keyDown(bins[1] as HTMLElement, { key: "End" });
    expect(bins[bins.length - 1]).toHaveFocus();
  });
});
