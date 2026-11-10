import { formatDayTime, formatRange, formatTime } from "./time";

const now = new Date(2026, 10, 4, 18, 5); // local Nov 4 18:05

describe("time formatting (§3.3)", () => {
  it("shows HH:mm today and 'MMM D HH:mm' otherwise", () => {
    expect(formatTime(new Date(2026, 10, 4, 9, 7).toISOString(), now)).toBe("09:07");
    expect(formatTime(new Date(2026, 10, 2, 16, 48).toISOString(), now)).toBe("Nov 2 16:48");
    expect(formatTime(new Date(2026, 10, 4, 10, 42, 6).toISOString(), now, true)).toBe("10:42:06");
  });
  it("always names the day in prompt rows", () => {
    expect(formatDayTime(new Date(2026, 10, 4, 10, 5).toISOString())).toBe("Nov 4 10:05");
  });
  it("names the day once for a same-day range", () => {
    expect(formatRange(new Date(2026, 10, 2, 16, 48).toISOString(), new Date(2026, 10, 2, 16, 57).toISOString(), now)).toBe(
      "Nov 2 16:48–16:57",
    );
    expect(formatRange(new Date(2026, 10, 2, 23, 50).toISOString(), new Date(2026, 10, 3, 0, 10).toISOString(), now)).toBe(
      "Nov 2 23:50–Nov 3 00:10",
    );
  });
});
