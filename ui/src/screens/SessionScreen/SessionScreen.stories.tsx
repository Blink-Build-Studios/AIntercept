import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { useState } from "react";
import requestFailed from "../../api/samples/request/failed";
import requestNormal from "../../api/samples/request/normal";
import requestNotCaptured from "../../api/samples/request/not_captured";
import requestRetained from "../../api/samples/request/retained";
import detailsNormal from "../../api/samples/request_details/normal";
import rawFailed from "../../api/samples/request_raw/failed";
import rawNormal from "../../api/samples/request_raw/normal";
import rawRetained from "../../api/samples/request_raw/retained";
import rawUnavailable from "../../api/samples/request_raw/unavailable";
import sessionCodex from "../../api/samples/session/codex";
import sessionDemo from "../../api/samples/session/demo";
import sessionError from "../../api/samples/session/error";
import sessionLive from "../../api/samples/session/live";
import eventsDemo from "../../api/samples/session_events/demo";
import promptsCodex from "../../api/samples/session_prompts/codex";
import promptsDemo from "../../api/samples/session_prompts/demo";
import promptsInFlight from "../../api/samples/session_prompts/in_flight";
import requestsCodex from "../../api/samples/session_requests/codex_channels";
import requestsDemo from "../../api/samples/session_requests/demo";
import stateDemo from "../../api/samples/session_state/demo";
import timelineDemo from "../../api/samples/session_timeline/demo";
import { readyRegion, type Region } from "../../lib/region";
import type { PromptItem, StepData } from "../../api/types";
import { longTurnPrompt, longTurnSteps } from "../../components/ProviderMark/providerSpecimens";
import type { DrawerTab } from "../../components/RequestDrawer/DrawerTabs";
import { curl } from "../../components/RequestDrawer/drawerData";
import { SessionScreen, type SessionScreenProps, type SessionTab } from "./SessionScreen";

const loading = { status: "loading", data: undefined } as const;
const whole = { from: timelineDemo.data.from, to: timelineDemo.data.to };

type Drawer = { detail: typeof requestNormal; raw: typeof rawNormal; tab?: DrawerTab };

/** The screen with its tabs, filters and pages working inside the story; data from the samples. */
function Screen({ initialTab = "prompts", openDrawer: drawer, ...over }: Partial<SessionScreenProps> & { initialTab?: SessionTab; openDrawer?: Drawer }) {
  const [tab, setTab] = useState<SessionTab>(initialTab);
  const [dtab, setDtab] = useState<DrawerTab>(drawer?.tab ?? "summary");
  const [rq, setRq] = useState({ q: "", kinds: [] as SessionScreenProps["requestFilter"]["kinds"], notCaptured: false });
  const [ev, setEv] = useState({ q: "", internal: false, ticks: true });
  const [open, setOpen] = useState<string[]>([]);
  const props: SessionScreenProps = {
    sessionId: sessionDemo.data.sessionId,
    session: readyRegion(sessionDemo.data),
    person: { name: null, href: "/people/p" },
    timeline: readyRegion(timelineDemo.data),
    state: readyRegion(stateDemo.data),
    prompts: readyRegion(promptsDemo),
    requests: readyRegion(requestsDemo),
    events: readyRegion(eventsDemo),
    onRetry: fn(),
    live: { status: "live", intervalS: 15 },
    paused: false,
    onPause: fn(),
    link: "https://aintercept.local/sessions/d7ee5e66",
    rangeMode: "whole",
    range: whole,
    onRange: fn(),
    onPrevRange: fn(),
    onNextRange: fn(),
    tab,
    onTab: setTab,
    page: 1,
    onPage: fn(),
    requestFilter: rq,
    onRequestFilter: setRq,
    eventFilter: ev,
    onEventFilter: setEv,
    onOpenRequest: fn(),
    drawer: drawer && {
      requestId: drawer.detail.data.requestId,
      detail: readyRegion(drawer.detail.data),
      raw: readyRegion(drawer.raw.data),
      details: readyRegion(detailsNormal.data),
      onRetry: fn(),
      tab: dtab,
      onTab: setDtab,
      onNavigate: fn(),
      curl: async () => curl(drawer.raw.data),
      link: "https://aintercept.local/sessions/d7ee5e66?req=1",
    },
    onCloseDrawer: fn(),
    eventJson: Object.fromEntries(open.map((id) => [id, { status: "loading", data: undefined } as Region<never>])),
    onToggleEvent: (id) => setOpen((o) => (o.includes(id) ? o.filter((x) => x !== id) : [...o, id])),
    onRetryEvent: fn(),
    now: sessionDemo.meta.asOf,
    ...over,
  };
  return <SessionScreen {...props} />;
}

const meta = { title: "Screens/SessionScreen", component: Screen, parameters: { layout: "fullscreen" } } satisfies Meta<typeof Screen>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The demo session (real traffic, sanitized), whole session, Prompts tab. */
export const Prompts: Story = {};
export const Requests: Story = { args: { initialTab: "requests" } };
export const Events: Story = { args: { initialTab: "events" } };
/** Live: the brush follows the last 15 min; a prompt in flight, its steps open. */
export const Live: Story = {
  args: {
    session: readyRegion(sessionLive.data),
    prompts: readyRegion(promptsInFlight),
    rangeMode: "following",
    range: { from: new Date(Date.parse(sessionDemo.meta.asOf) - 15 * 60_000).toISOString(), to: sessionDemo.meta.asOf },
  },
};
/** First load: every region's skeleton at its loaded size. */
export const Loading: Story = { args: { session: loading, timeline: loading, state: loading, prompts: loading, requests: loading, events: loading } };
/** The header query failed with nothing to show: the page's ErrorState. */
export const LoadFailed: Story = {
  args: {
    session: { status: "error", data: undefined, error: { message: sessionError.error.message, queryId: sessionError.error.queryId ?? undefined } },
    timeline: loading,
    state: loading,
    prompts: loading,
    requests: loading,
    events: loading,
  },
};
/** A step opened in the request drawer. */
export const Drawer: Story = { args: { openDrawer: { detail: requestNormal, raw: rawNormal } } };
export const DrawerNotCaptured: Story = { args: { openDrawer: { detail: requestNotCaptured, raw: rawUnavailable } } };
export const DrawerFailed: Story = { args: { openDrawer: { detail: requestFailed, raw: rawFailed } } };
/** Experimentation mode: the drawer on Raw with bodies kept. */
export const DrawerRetained: Story = { args: { openDrawer: { detail: requestRetained, raw: rawRetained, tab: "raw" } } };

/** A Codex thread (handoff §11L.5): the ProviderMark with its client, only Codex's facts and stats; Claude Code's lanes absent. */
export const CodexPrompts: Story = { args: { session: readyRegion(sessionCodex.data), prompts: readyRegion(promptsCodex), requests: readyRegion(requestsCodex) } };
/** Codex's Requests (§11L.6): chips for its channels with calls, the hidden-calls checkbox, details shaped per channel. */
export const CodexRequests: Story = { args: { ...CodexPrompts.args, initialTab: "requests" } };

// A 45-step turn (§11L.7), built from CompProviders' step specimens: reads, a failing test
// loop (a run), a patch, more commands, a closing text step.
const turn: StepData[] = Array.from({ length: 45 }, (_, i): StepData => {
  const base = longTurnSteps.first.step;
  const t = new Date(Date.parse(base.t) + i * 25_000).toISOString();
  const step = { ...base, requestId: `turn-step-${i + 1}`, t, durationMs: 20_000 };
  if (i === 20) return { ...step, does: "patch", patches: [{ file: "src/timer.ts", added: 12, removed: 3 }] };
  if (i === 44) return { ...step, does: "text", text: "Tests pass now. The flake came from", stopReason: "end_turn" };
  if (i === 30) return { ...step, does: "mcp" };
  return { ...step, commands: i < 20 ? ["rg"] : ["npm test"], exitCodes: i > 20 && i < 30 ? [1] : [0] };
});
const longTurn: PromptItem = { ...longTurnPrompt, steps: turn, prompt: { ...longTurnPrompt.prompt, steps: turn.length } };
/** A long turn opened: the StepStrip under the prompt, numbered steps, command-only runs collapsed. */
export const LongTurn: Story = {
  args: {
    session: readyRegion(sessionCodex.data),
    prompts: readyRegion({ ...promptsCodex, data: [longTurn] }),
    jumpTo: { index: 1, seq: 1 },
  },
};
