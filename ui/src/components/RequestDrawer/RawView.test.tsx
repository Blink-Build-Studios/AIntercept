import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import normal from "../../api/samples/request_raw/normal";
import retained from "../../api/samples/request_raw/retained";
import { RawView } from "./RawView";

// §12: "Credentials never appear in Raw (server-redacted; a client test asserts `authorization` is redacted)."
describe("Raw never shows a credential", () => {
  for (const [name, sample] of [["normal", normal], ["retained", retained]] as const) {
    it(`${name}: authorization shows only its redacted marker`, () => {
      const { container } = render(<RawView raw={sample.data} />);
      const [key] = screen.getAllByText("authorization");
      const value = key?.nextElementSibling?.textContent ?? "";
      expect(value.toLowerCase()).toContain("redacted");
      const text = container.textContent ?? "";
      expect(text).not.toMatch(/Bearer\s+\S{8,}|sk-ant-[\w-]{8,}|x-api-key:\s*\S{8,}/i);
    });
  }
});
