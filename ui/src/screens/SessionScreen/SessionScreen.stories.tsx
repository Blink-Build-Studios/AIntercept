import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import { useState } from "react";
import requestFailed from "../../api/samples/request/failed";
import requestNormal from "../../api/samples/request/normal";
import requestRetained from "../../api/samples/request/retained";
import detailsNormal from "../../api/samples/request_details/normal";
import rawFailed from "../../api/samples/request_raw/failed";
import rawNormal from "../../api/samples/request_raw/normal";
import rawRetained from "../../api/samples/request_raw/retained";
import sessionCodex from "../../api/samples/session/codex";
import sessionDemo from "../../api/samples/session/demo";
import sessionError from "../../api/samples/session/error";
import sessionLive from "../../api/samples/session/live";
import eventsDemo from "../../api/samples/session_events/demo";
import turnsCodex from "../../api/samples/session_turns/codex";
import turnsDemo from "../../api/samples/session_turns/demo";
import turnsLive from "../../api/samples/session_turns/live";
import turnsSubagents from "../../api/samples/session_turns/subagents";
import turnsWithContent from "../../api/samples/session_turns/with_content";
import requestsCodex from "../../api/samples/session_requests/codex_channels";
import requestsDemo from "../../api/samples/session_requests/demo";
import stateDemo from "../../api/samples/session_state/demo";
import timelineDemo from "../../api/samples/session_timeline/demo";
import { readyRegion, type Region } from "../../lib/region";
import type { DrawerTab } from "../../components/RequestDrawer/DrawerTabs";
import { curl } from "../../components/RequestDrawer/drawerData";
import { SessionScreen, type SessionScreenProps, type SessionTab } from "./SessionScreen";

const loading = { status: "loading", data: undefined } as const;
const whole = { from: timelineDemo.data.from, to: timelineDemo.data.to };

type Drawer = { detail: typeof requestNormal; raw: typeof rawNormal; tab?: DrawerTab };

/** The screen with its tabs, filters and pages working inside the story; data from the samples. */
function Screen({ initialTab = "turns", openDrawer: drawer, ...over }: Partial<SessionScreenProps> & { initialTab?: SessionTab; openDrawer?: Drawer }) {
  const [tab, setTab] = useState<SessionTab>(initialTab);
  const [dtab, setDtab] = useState<DrawerTab>(drawer?.tab ?? "summary");
  const [rq, setRq] = useState({ q: "", kinds: [] as SessionScreenProps["requestFilter"]["kinds"], notCaptured: false });
  const [ev, setEv] = useState({ q: "", internal: false });
  const [open, setOpen] = useState<string[]>([]);
  const props: SessionScreenProps = {
    sessionId: sessionDemo.data.sessionId,
    session: readyRegion(sessionDemo.data),
    person: { name: null, href: "/people/p" },
    timeline: readyRegion(timelineDemo.data),
    state: readyRegion(stateDemo.data),
    turns: readyRegion(turnsDemo),
    sessionHref: (id) => `/sessions/${id}`,
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

/** The demo session (real traffic, sanitized), whole session, Turns tab (§11R). */
export const Turns: Story = {};
export const Requests: Story = { args: { initialTab: "requests" } };
export const Events: Story = { args: { initialTab: "events" } };
/** Live: the brush follows the last 15 min; a turn running, open. */
export const Live: Story = {
  args: {
    session: readyRegion(sessionLive.data),
    turns: readyRegion(turnsLive),
    rangeMode: "following",
    range: { from: new Date(Date.parse(sessionDemo.meta.asOf) - 15 * 60_000).toISOString(), to: sessionDemo.meta.asOf },
  },
};
/** First load: every region's skeleton at its loaded size. */
export const Loading: Story = { args: { session: loading, timeline: loading, state: loading, turns: loading, requests: loading, events: loading } };
/** The header query failed with nothing to show: the page's ErrorState. */
export const LoadFailed: Story = {
  args: {
    session: { status: "error", data: undefined, error: { message: sessionError.error.message, queryId: sessionError.error.queryId ?? undefined } },
    timeline: loading,
    state: loading,
    turns: loading,
    requests: loading,
    events: loading,
  },
};
/** A step opened in the request drawer. */
export const Drawer: Story = { args: { openDrawer: { detail: requestNormal, raw: rawNormal } } };
export const DrawerFailed: Story = { args: { openDrawer: { detail: requestFailed, raw: rawFailed } } };
/** Experimentation mode: the drawer on Raw with bodies kept. */
export const DrawerRetained: Story = { args: { openDrawer: { detail: requestRetained, raw: rawRetained, tab: "raw" } } };

/** A Codex thread (handoff §11L.5): the ProviderMark with its client, only Codex's facts and stats; Claude Code's lanes absent. */
export const CodexTurns: Story = { args: { session: readyRegion(sessionCodex.data), turns: readyRegion(turnsCodex), requests: readyRegion(requestsCodex) } };
/** Codex's Requests (§11L.6): chips for its channels with calls, the hidden-calls checkbox, details shaped per channel. */
export const CodexRequests: Story = { args: { ...CodexTurns.args, initialTab: "requests" } };

/** Recorded content: the prompt's text, paths in the groups, the reply's preview. */
export const WithContent: Story = { args: { turns: readyRegion(turnsWithContent), jumpTo: { index: 1, seq: 1 } } };
/** A turn with MCP, web and a spawned subagent, its ⤷ row under it. */
export const Subagents: Story = { args: { turns: readyRegion(turnsSubagents), jumpTo: { index: 2, seq: 1 } } };
