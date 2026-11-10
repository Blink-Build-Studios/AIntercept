import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { unknownTip } from "../../lib/unknown";
import { Button } from "../Button/Button";
import { Tooltip, TooltipProvider } from "./Tooltip";

const tip = () => screen.queryByRole("tooltip");
const advance = (ms: number) => act(() => void vi.advanceTimersByTime(ms));

describe("Tooltip (handoff §6B.2–6B.3)", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("[6B.11-12] A mark opens after 120 ms, not before 100 ms; the next mark opens at once", () => {
    render(
      <TooltipProvider>
        <Tooltip kind="mark" content={{ body: "bin 1" }}>
          <span>a</span>
        </Tooltip>
        <Tooltip kind="mark" content={{ body: "bin 2" }}>
          <span>b</span>
        </Tooltip>
      </TooltipProvider>,
    );
    fireEvent.pointerEnter(screen.getByText("a"));
    advance(100);
    expect(tip()).toBeNull();
    advance(20);
    expect(tip()).toHaveTextContent("bin 1");
    fireEvent.pointerLeave(screen.getByText("a"));
    fireEvent.pointerEnter(screen.getByText("b"));
    expect(tip()).toHaveTextContent("bin 2");
    expect(screen.getAllByRole("tooltip")).toHaveLength(1);
  });

  it("[6B.11-13] A cell opens at 400 ms; the pointer can move into it without it closing", () => {
    render(
      <TooltipProvider>
        <Tooltip content={{ rows: [{ key: "Total", value: "9,312 ms" }] }}>
          <span>9.3 s</span>
        </Tooltip>
      </TooltipProvider>,
    );
    fireEvent.pointerEnter(screen.getByText("9.3 s"));
    advance(399);
    expect(tip()).toBeNull();
    advance(1);
    const box = screen.getByRole("tooltip");
    fireEvent.pointerLeave(screen.getByText("9.3 s"));
    advance(50);
    fireEvent.pointerEnter(box);
    advance(500);
    expect(tip()).not.toBeNull();
    expect(getComputedStyle(box).userSelect).not.toBe("none");
    fireEvent.pointerLeave(box);
    advance(100);
    expect(tip()).toBeNull();
  });

  it("[6B.11-14] Keyboard focus shows it at once with aria-describedby; Esc closes it and keeps focus", () => {
    const onKeyDown = vi.fn();
    render(
      <TooltipProvider>
        <Tooltip kind="mark" content={{ body: "Nov 3 10:00–10:30" }}>
          <button type="button" onKeyDown={onKeyDown}>
            bin
          </button>
        </Tooltip>
      </TooltipProvider>,
    );
    const bin = screen.getByText("bin");
    act(() => bin.focus());
    const box = screen.getByRole("tooltip");
    expect(bin).toHaveAttribute("aria-describedby", box.id);
    fireEvent.keyDown(bin, { key: "Escape" });
    expect(tip()).toBeNull();
    expect(bin).toHaveFocus();
    expect(onKeyDown).not.toHaveBeenCalled();
    // A second Esc reaches the element's own handler (clear the brush, close the drawer).
    fireEvent.keyDown(bin, { key: "Escape" });
    expect(onKeyDown).toHaveBeenCalledTimes(1);
  });

  it("[6B.11-17] A null value renders — and its tooltip says why, never 0", () => {
    render(
      <TooltipProvider>
        <Tooltip content={unknownTip("Repo", "not parsed before Nov 2")} open>
          <span>—</span>
        </Tooltip>
      </TooltipProvider>,
    );
    expect(screen.getByRole("tooltip")).toHaveTextContent("Repo—  not parsed before Nov 2".replace(/\s+/g, " "), { normalizeWhitespace: true });
    expect(screen.getByRole("tooltip")).not.toHaveTextContent(/\b0\b/);
  });

  it("[6B.11-18] A disabled control is focusable, ignores clicks and shows its reason", () => {
    const onClick = vi.fn();
    render(
      <TooltipProvider>
        <Button disabled disabledReason="Loading page 3…" onClick={onClick}>
          Next
        </Button>
      </TooltipProvider>,
    );
    const button = screen.getByRole("button", { name: "Next" });
    expect(button).toHaveAttribute("aria-disabled", "true");
    expect(button).not.toHaveAttribute("disabled");
    act(() => button.focus());
    expect(button).toHaveFocus();
    fireEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
    expect(screen.getByRole("tooltip")).toHaveTextContent("Loading page 3…");
  });

  it("A truncate tooltip only opens when the text is cut off", () => {
    render(
      <TooltipProvider>
        <Tooltip kind="truncate" content={{ body: "full" }}>
          <span>short</span>
        </Tooltip>
      </TooltipProvider>,
    );
    fireEvent.pointerEnter(screen.getByText("short"));
    advance(1000);
    expect(tip()).toBeNull();
  });
});
