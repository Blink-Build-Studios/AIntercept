import { formatAge, formatBytes, formatCount, formatDuration, formatTokens } from "./format";

describe("formatCount", () => {
  it.each([
    [12, "12"],
    [21_305, "21,305"],
    [99_999, "99,999"],
    [384_000, "384k"],
    [1_100_000, "1.1M"],
    [140_900_000, "140.9M"],
    [2_000_000_000, "2B"],
  ])("%i -> %s", (n, out) => expect(formatCount(n)).toBe(out));
});

describe("formatDuration", () => {
  it.each([
    [71, "71 ms"],
    [4_800, "4.8 s"],
    [600, "600 ms"],
    [22_400, "22 s"],
    [9_300, "9.3 s"],
    [24 * 60_000, "24m 0s"],
    [71_000, "1m 11s"],
    [243_000, "4m 3s"],
    [5_000, "5 s"],
    [100_000, "1m 40s"],
    [30_000_000, "8h 20m"],
    [172_800_000, "2d 0h"],
  ])("%i ms -> %s", (ms, out) => expect(formatDuration(ms)).toBe(out));
});

describe("formatAge", () => {
  it("uses minutes under an hour, then two units", () => {
    expect(formatAge(4 * 60_000 + 59_000)).toBe("4m");
    expect(formatAge(86_400_000)).toBe("1d 0h");
  });
});

describe("formatBytes", () => {
  it.each([
    [44, "44 B"],
    [2280, "2.3 KB"],
    [400, "400 B"],
    [4_100, "4.1 KB"],
    [15_000, "15 KB"],
    [412_400, "412 KB"],
    [37_200_000, "37.2 MB"],
    [221_444, "221 KB"],
    [37_200_000, "37.2 MB"],
  ])("%i -> %s", (n, out) => expect(formatBytes(n)).toBe(out));
});

describe("formatTokens", () => {
  it.each([
    [850, "total", "850"],
    [900, "total", "900"],
    [2_600, "total", "2.6k"],
    [9_960, "total", "10k"],
    [39_200, "total", "39k"],
    [905_000, "total", "905k"],
    [999_700, "total", "1.0M"],
    [1_124_394, "total", "1.1M"],
    [380, "call", "380"],
    [900, "call", "900"],
    [1_200, "call", "1.2k"],
    [43_000, "call", "43.0k"],
    [41_200, "call", "41.2k"],
    [389_080, "call", "389.1k"],
    [1_240_000, "call", "1.2M"],
  ] as const)("%i (%s) -> %s", (n, scope, out) => expect(formatTokens(n, scope)).toBe(out));
});
