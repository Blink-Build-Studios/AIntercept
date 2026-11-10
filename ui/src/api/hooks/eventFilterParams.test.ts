import { describe, expect, it } from "vitest";
import { eventFilterParams } from "./useSessionPage";

describe("eventFilterParams (the Events tab's filters as query parameters)", () => {
  it("asks for client internals only when they're shown (the API hides them by default)", () => {
    expect(eventFilterParams(undefined, true, true).internal).toBe(true);
    expect(eventFilterParams(undefined, false, true).internal).toBeUndefined();
  });
});
