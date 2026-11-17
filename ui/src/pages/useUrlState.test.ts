import { describe, expect, it } from "vitest";
import { parseUrlState, patchUrlState } from "./useUrlState";

describe("URL state (handoff §7)", () => {
  it("round-trips range, tab, filters, page, toggles and the drawer", () => {
    const p = patchUrlState(new URLSearchParams(), {
      from: "2026-11-03T10:00:00Z",
      to: "2026-11-03T12:00:00Z",
      tab: "requests",
      kind: ["model", "count"],
      req: "r1",
      dtab: "raw",
    });
    const withPage = patchUrlState(p, { page: 3 });
    expect(parseUrlState(new URLSearchParams(withPage.toString()))).toMatchObject({
      from: "2026-11-03T10:00:00Z",
      to: "2026-11-03T12:00:00Z",
      tab: "requests",
      kind: ["model", "count"],
      page: 3,
      req: "r1",
      dtab: "raw",
    });
  });

  it("a new range or filter resets the page; opening the drawer doesn't", () => {
    const p = new URLSearchParams("page=4&tab=requests");
    expect(patchUrlState(p, { from: "a", to: "b" }).get("page")).toBeNull();
    expect(patchUrlState(p, { kind: ["model"] }).get("page")).toBeNull();
    expect(patchUrlState(p, { req: "r1" }).get("page")).toBe("4");
    expect(patchUrlState(p, { hidden: true }).get("page")).toBeNull();
  });

  it("about this data is open only with about=1; closing removes it", () => {
    expect(parseUrlState(new URLSearchParams("")).about).toBe(false);
    const open = patchUrlState(new URLSearchParams("tab=requests"), { about: true });
    expect(open.toString()).toBe("tab=requests&about=1");
    expect(parseUrlState(open).about).toBe(true);
    expect(patchUrlState(open, { about: false }).toString()).toBe("tab=requests");
  });

  it("a jump to a prompt holds until the page, range or tab changes; the drawer keeps it", () => {
    const jumped = patchUrlState(new URLSearchParams("tab=requests&page=3"), { tab: undefined, prompt: "63" });
    expect(jumped.toString()).toBe("prompt=63");
    expect(parseUrlState(jumped).prompt).toBe("63");
    expect(patchUrlState(jumped, { req: "r1" }).get("prompt")).toBe("63");
    expect(patchUrlState(jumped, { page: 2 }).get("prompt")).toBeNull();
    expect(patchUrlState(jumped, { from: "a", to: "b" }).get("prompt")).toBeNull();
  });
});
