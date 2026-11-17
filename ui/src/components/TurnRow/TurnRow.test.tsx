import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "../Tooltip/Tooltip";
import { TURN_SPECIMENS } from "./CompTurns.canvas.stories";
import { TurnRow } from "./TurnRow";
import { WorkGroup } from "./WorkGroup";

const row = (expanded: boolean, onToggle = vi.fn()) => {
  render(
    <TooltipProvider>
      <div role="treegrid" aria-label="Turns">
        <TurnRow turn={TURN_SPECIMENS.open} work={["read 5 files · ran npm test ×4: ", { failed: "3 failed" }, ", then passed"]} expanded={expanded} onToggle={onToggle}>
          <WorkGroup kind="read" title="Read 5 files" meta=".ts ×5" expanded={false} onToggle={vi.fn()} />
        </TurnRow>
      </div>
    </TooltipProvider>,
  );
  return { onToggle, button: screen.getAllByRole("row")[0] as HTMLElement };
};

describe("TurnRow (handoff §11R: whole row toggles; → opens, ← closes)", () => {
  it("toggles on click", () => {
    const { onToggle, button } = row(false);
    fireEvent.click(button);
    expect(onToggle).toHaveBeenCalledWith("t14");
  });

  it("→ opens a closed turn and ← closes an open one; the other arrow leaves it", () => {
    const closed = row(false);
    fireEvent.keyDown(closed.button, { key: "ArrowLeft" });
    expect(closed.onToggle).not.toHaveBeenCalled();
    fireEvent.keyDown(closed.button, { key: "ArrowRight" });
    expect(closed.onToggle).toHaveBeenCalledTimes(1);
  });

  it("shows its groups only when open, and says so", () => {
    const { button } = row(true);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("Read 5 files")).toBeTruthy();
  });

  it("puts a failure count in its own span, in ink, never danger", () => {
    row(false);
    const failed = screen.getByText("3 failed");
    expect(failed.className).toContain("text-ink");
    expect(failed.className).not.toContain("danger");
  });
});

describe("TurnRow friction (handoff §11S: ✕ interrupted, ! failed calls, both --ink)", () => {
  const friction = (over: Partial<typeof TURN_SPECIMENS.default>, failed?: number) => {
    render(
      <TooltipProvider>
        <div role="treegrid" aria-label="Turns">
          <TurnRow turn={{ ...TURN_SPECIMENS.default, ...over }} failed={failed} work={["Read ×1"]} expanded={false} onToggle={vi.fn()} />
        </div>
      </TooltipProvider>,
    );
    return screen.getAllByRole("cell").at(-1) as HTMLElement;
  };

  it("an interrupted turn reads ✕", () => {
    const cell = friction({ friction: { ...TURN_SPECIMENS.default.friction, interrupted: true } });
    expect(cell.textContent).toBe("✕");
    expect(cell.getAttribute("aria-label")).toBe("interrupted");
  });

  it("failed calls read !, the count in words", () => {
    const cell = friction({}, 2);
    expect(cell.textContent).toBe("!");
    expect(cell.getAttribute("aria-label")).toBe("2 failed calls");
  });

  it("frustrated and destructive are gone, and so is the Cost cell", () => {
    const cell = friction({ friction: { frustrated: true, interrupted: false, destructive: 3 }, costUsd: 1.92 });
    expect(cell.textContent).toBe("");
    expect(screen.queryByText("$1.92")).toBeNull();
    expect(screen.getAllByRole("cell")).toHaveLength(8); // the twisty is aria-hidden
  });
});

describe("WorkGroup exit codes (handoff §11R: listed on the right, never red)", () => {
  it("lists a few in order and tallies many", async () => {
    const { exitLine } = await import("./WorkGroup");
    expect(exitLine([1, 1, 1, 0])).toBe("exit 1 · exit 1 · exit 1 · exit 0");
    expect(exitLine([0, 0, 1, 0, 1, 0])).toBe("exit 0 ×4 · exit 1 ×2");
  });
});
