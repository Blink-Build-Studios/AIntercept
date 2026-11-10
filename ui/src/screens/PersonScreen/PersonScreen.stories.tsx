import type { Meta, StoryObj } from "@storybook/react";
import { fn } from "@storybook/test";
import person from "../../api/samples/person/demo";
import personLinked from "../../api/samples/person/linked_codex";
import sessionsCodex from "../../api/samples/person_sessions/codex_threads";
import requestsDemo from "../../api/samples/person_requests/demo";
import requestsEmpty from "../../api/samples/person_requests/empty";
import requestsNotCaptured from "../../api/samples/person_requests/not_captured";
import sessionsDemo from "../../api/samples/person_sessions/demo";
import sessionsEmpty from "../../api/samples/person_sessions/empty";
import summary from "../../api/samples/person_summary/demo";
import timeline from "../../api/samples/person_timeline/demo";
import requestDetail from "../../api/samples/request/demo";
import requestDetails from "../../api/samples/request_details/demo";
import requestRaw from "../../api/samples/request_raw/demo";
import waitingStatus from "../../api/samples/status/waiting";
import { readyRegion } from "../../lib/region";
import { curl } from "../../components/RequestDrawer/drawerData";
import { waitingSteps } from "../../components/WaitingChecklist/WaitingChecklist";
import { PersonScreen, type PersonScreenProps } from "./PersonScreen";

const NOW = person.meta.asOf;
const loading = { status: "loading", data: undefined } as const;

const base: PersonScreenProps = {
  now: NOW,
  live: { status: "live", intervalS: 15 },
  paused: false,
  onPause: fn(),
  window: "14d",
  onWindow: fn(),
  person: readyRegion(person.data),
  summary: readyRegion(summary.data),
  timeline: readyRegion(timeline.data),
  range: null,
  onRange: fn(),
  tab: "sessions",
  onTab: fn(),
  sessions: readyRegion(sessionsDemo),
  requests: readyRegion(requestsDemo),
  page: 1,
  onPage: fn(),
  q: "",
  onQ: fn(),
  kinds: [],
  onKinds: fn(),
  notCaptured: false,
  onNotCaptured: fn(),
  onRetry: fn(),
  peopleHref: "#people",
  sessionHref: (id) => `#sessions/${id}`,
  onOpenRequest: fn(),
};

const meta = { title: "Screens/PersonScreen", component: PersonScreen, parameters: { layout: "fullscreen" }, args: base } satisfies Meta<typeof PersonScreen>;
export default meta;
type Story = StoryObj<typeof meta>;

/** The demo person (real traffic, sanitized): live, with a coverage gap. */
export const Sessions: Story = {};
export const AllRequests: Story = { args: { tab: "requests" } };
/** A person linked by email (handoff §11L.3): the ProviderMark names both agents; All agents, then a Claude Code and a Codex row, each with only its own fields. */
export const Linked: Story = { args: { person: readyRegion(personLinked.data), sessions: readyRegion(sessionsCodex), agentScope: "all", onAgentScope: fn() } };
/** A linked person scoped to Codex (§11M 3.3): the sessions table without Agent; All requests with Codex's chips. */
export const LinkedCodex: Story = { args: { person: readyRegion(personLinked.data), sessions: readyRegion(sessionsCodex), agentScope: "codex", onAgentScope: fn() } };
/** A linked person's All requests in All agents: the shared chips and the agent-dot column. */
export const LinkedAllRequests: Story = { args: { person: readyRegion(personLinked.data), tab: "requests", agentScope: "all", onAgentScope: fn(), onShowHidden: fn() } };
/** "view N calls" (or the timeline's hatch): All requests filtered to calls the proxy didn't capture (handoff §11E.2). */
export const NotCaptured: Story = { args: { tab: "requests", notCaptured: true, requests: readyRegion(requestsNotCaptured) } };
/** The filter matched nothing in the range: what coverage says, and ways out. */
export const NoUncaptured: Story = {
  args: {
    tab: "requests",
    notCaptured: true,
    range: { from: "2026-11-04T08:00:00Z", to: "2026-11-04T10:10:00Z" },
    requests: { status: "empty", data: requestsEmpty },
  },
};
/** "About this data" open over the page (`?about=1`). */
export const AboutOpen: Story = {
  args: {
    about: {
      open: true,
      contentRetained: false,
      products: [
        { agent: "claude-code", tokenAccuracy: "exact", sessions: 11 },
        { agent: "codex", tokenAccuracy: "exact", sessions: 6 },
      ],
      onOpen: fn(),
      onClose: fn(),
    },
  },
};
/** A request opened from All requests: the drawer over the page. */
export const AllRequestsDrawer: Story = {
  args: {
    tab: "requests",
    drawer: {
      requestId: requestDetail.data.requestId,
      detail: readyRegion(requestDetail.data),
      raw: readyRegion(requestRaw.data),
      details: readyRegion(requestDetails.data),
      onRetry: fn(),
      tab: "summary",
      onTab: fn(),
      onClose: fn(),
      onNavigate: fn(),
      curl: async () => curl(requestRaw.data),
    },
  },
};
/** First load: every region shows its skeleton; the header crumb is a skeleton word. */
export const Loading: Story = { args: { person: loading, summary: loading, timeline: loading, sessions: loading, requests: loading, live: { status: "connecting" } } };
/** No traffic has reached the proxy yet. */
export const Waiting: Story = { args: { waiting: { steps: waitingSteps(waitingStatus.data, new Date(NOW)), checkedAt: NOW }, live: { status: "waiting" } } };
/** The scope is live but nothing has arrived for over 3 minutes. */
export const Delayed: Story = {
  args: { banner: { kind: "delayed", since: person.meta.lastDataAt ?? NOW }, live: { status: "delayed", delayedMs: 4 * 60_000 } },
};
/** A poll failed: the data stays, with Retry now and the backoff. */
export const RefreshFailed: Story = {
  args: { banner: { kind: "refreshFailed", since: NOW, retryInS: 10, onRetry: fn() }, live: { status: "retrying" } },
};
/** The brush covers no activity: the nearest activity either side. */
export const EmptyRange: Story = {
  args: {
    range: { from: "2026-11-02T01:00:00Z", to: "2026-11-02T03:00:00Z" },
    sessions: readyRegion(sessionsEmpty),
    requests: readyRegion(requestsEmpty),
  },
};
