import { render } from "@testing-library/react";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { HeaderList, middleEllipsis } from "./HeaderList";
import { HEADER_SPECIMENS } from "./headerSpecimens";

describe("HeaderList (§11H.3, §11L.8)", () => {
  it("dims no prefix: every name is one run of ink, in wire order", () => {
    const headers = HEADER_SPECIMENS.anthropicResponse;
    const { container } = render(createElement(HeaderList, { headers }));
    const names = [...container.querySelectorAll("dt")];
    expect(names.map((n) => n.textContent)).toEqual(headers.map((h) => h.name));
    expect(container.querySelector("dt .text-ink-faint")).toBeNull();
  });

  it("over the cap, a middle ellipsis keeps both ends", () => {
    expect(middleEllipsis("short")).toBe("short");
    const long = "a".repeat(30) + "-" + "b".repeat(30);
    const shown = middleEllipsis(long, 21);
    expect(shown).toHaveLength(21);
    expect(shown.startsWith("aaaaaaaaaa…")).toBe(true);
    expect(shown.endsWith("bbbbbbbbbb")).toBe(true);
  });
});
