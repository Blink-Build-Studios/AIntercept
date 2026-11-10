import delayed from "../api/samples/status/delayed";
import probesOnly from "../api/samples/status/probes_only";
import receiving from "../api/samples/status/receiving";
import waiting from "../api/samples/status/waiting";
import { liveStatusFrom } from "./live";

describe("liveStatusFrom", () => {
  it("waits until the first real request", () => {
    expect(liveStatusFrom(waiting).status).toBe("waiting");
    expect(liveStatusFrom(probesOnly).status).toBe("waiting");
  });

  it("is live when data is recent", () => {
    expect(liveStatusFrom(receiving)).toEqual({ status: "live" });
  });

  it("is delayed once data is older than the threshold, with the age", () => {
    // delayed sample: last row 17:45:00.002, asOf 18:05:00
    expect(liveStatusFrom(delayed)).toEqual({ status: "delayed", delayedMs: 19 * 60_000 + 59_998 });
  });
});
