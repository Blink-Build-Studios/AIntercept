import { describe, expect, it } from "vitest";
import session from "../../api/samples/session/codex";
import requests from "../../api/samples/session_requests/codex_channels";
import hidden from "../../api/samples/session_requests/codex_hidden";
import { requestChannels, requestDetail, sessionTabs, tabHint } from "./sessionView";

const counts = { prompts: 1, modelCalls: 203, requests: 615, events: 58, compactions: 0 };

describe("session tabs (handoff §11L.7)", () => {
  it("label each tab with its count and unit", () => {
    expect(sessionTabs(counts).map((t) => `${t.label} ${t.count}`)).toEqual(["Prompts 1 · 203 steps", "Requests 203 calls · 412 other", "Events 58"]);
  });

  it("Codex's Prompts hint says it calls a prompt a turn", () => {
    expect(tabHint("prompts", session.data, counts, undefined, "Sam")).toBe("What Sam asked, and the work each message started: 1 prompt, 203 model calls as steps. Codex calls a prompt a turn.");
  });
});

describe("requests (handoff §11L.6)", () => {
  it("chips are the session's declared channels with their counts", () => {
    expect(requestChannels(session.data, requests.kindCounts).map((c) => [c.channel, c.count])).toEqual([
      ["model", 5],
      ["analytics", 0],
      ["metrics", 0],
      ["usage", 1],
      ["catalog", 1],
    ]);
  });

  it("a model call's detail is its step (the # column holds the prompt, §11M 1.5), model, tokens and stop; the endpoint for others", () => {
    const [call] = requests.data;
    const prewarm = hidden.data.find((r) => r.hiddenKind === "prewarm");
    expect(requestDetail(call!)).toMatchObject({ head: "step 1", strong: true, facts: ["gpt-6.1-sol medium", "1.2k → 50"], stop: "completed" });
    expect(requestDetail(call!, { withPrompt: true })).toMatchObject({ head: "#1 · step 1" });
    expect(requestDetail(prewarm!)).toEqual({ head: "prewarm", facts: ["gpt-6.1-sol", "no output"] });
    expect(requestDetail(requests.data.find((r) => r.kind === "usage")!)).toEqual({ head: "wham/usage" });
  });
});
