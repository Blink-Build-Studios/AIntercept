import { describe, expect, it } from "vitest";
import { documentTitle } from "./useDocumentTitle";

describe("tab titles (§11H.1)", () => {
  it("page first, brand last; ● while a session is live; the brand alone on People", () => {
    expect(documentTitle()).toBe("AIntercept");
    expect(documentTitle("Unattributed traffic")).toBe("Unattributed traffic · AIntercept");
    expect(documentTitle("partner-readiness", true)).toBe("● partner-readiness · AIntercept");
  });
});
