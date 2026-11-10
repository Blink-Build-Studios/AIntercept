import { delay, HttpResponse } from "msw";
import errorSample from "../samples/status/error";

// The Storybook latency toolbar (handoff §6A.11). Every handler goes through
// `respond`, so every screen can be reviewed under each mode.
export const LATENCY_MODES = ["instant", "fast", "normal", "slow", "timeout", "error", "offline"] as const;
export type LatencyMode = (typeof LATENCY_MODES)[number];

const DELAY_MS: Record<LatencyMode, number> = {
  instant: 0,
  fast: 120,
  normal: 600,
  slow: 9_000,
  timeout: 35_000, // past the client's 30 s abort (api/client.ts)
  error: 300,
  offline: 0,
};

let mode: LatencyMode = "fast";

export function setLatency(next: LatencyMode) {
  mode = next;
}

export async function respond(body: unknown) {
  await delay(DELAY_MS[mode]);
  if (mode === "offline") return HttpResponse.error();
  // The API's own 500 body, generated from api/tests (typed ErrorResponse).
  if (mode === "error") return HttpResponse.json(errorSample, { status: 500 });
  return HttpResponse.json(body as Record<string, unknown>);
}
