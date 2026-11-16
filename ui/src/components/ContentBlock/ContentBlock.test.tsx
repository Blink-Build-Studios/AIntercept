import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { collapses, ContentBlock, EXPAND_LINES, previewLines } from "./ContentBlock";

describe("ContentBlock rules (handoff §11R, CompTurns)", () => {
  it("collapses over 12 lines or 4 KB; shorter content shows in full", () => {
    expect(collapses(12, 4096)).toBe(false);
    expect(collapses(13, 100)).toBe(true);
    expect(collapses(3, 4097)).toBe(true);
  });

  it("previews the head of files, prompts and replies, the tail of command output", () => {
    const lines = ["1", "2", "3", "4", "5", "6"];
    expect(previewLines(lines, "head")).toEqual(["1", "2", "3", "4"]);
    expect(previewLines(lines, "tail")).toEqual(["3", "4", "5", "6"]);
  });

  it("expands to at most 400 lines, then offers the raw view", () => {
    const lines = Array.from({ length: 500 }, (_, i) => `line ${i}`);
    render(<ContentBlock label="Tool result" meta="3,880 lines" content={{ lines, total: 3_880, bytes: 200_000, from: "tail" }} onOpenRaw={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "expand" }));
    const text = document.querySelector(".content-block-text")?.textContent ?? "";
    expect(text.split("\n")).toHaveLength(EXPAND_LINES);
    expect(screen.getByRole("button", { name: "open in raw ›" })).toBeTruthy();
  });

  it("without content shows the header and the note only: no preview, no expand", () => {
    render(<ContentBlock label="Tool result · Bash npm" meta="exit 1 · 18 s" content={null} note="program and exit code from Claude Code’s analytics" />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(document.querySelector(".content-block-body")).toBeNull();
  });
});
