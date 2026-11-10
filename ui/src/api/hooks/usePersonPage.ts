// The person page's data and URL wiring (handoff §7.2): every region's query, the
// brush range (it filters the ribbon, footprint and both tabs), the tab, filters,
// page and the drawer, all in the URL; live polling for every region.
import { pickBin, useTrackPx } from "../../lib/bins";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { DrawerTab } from "../../components/RequestDrawer/DrawerTabs";
import type { Seed } from "../../components/RequestDrawer/drawerData";
import { waitingSteps } from "../../components/WaitingChecklist/WaitingChecklist";
import { toRegion } from "../../lib/region";
import { aboutState, useAboutProducts } from "./aboutState";
import { useUrlState } from "../../pages/useUrlState";
import type { AgentScopeValue } from "../../components/AgentScope/AgentScope";
import { SHARED_CHANNELS } from "../../screens/SessionScreen/sessionView";
import { WINDOWS, type PersonScreenProps, type PersonTab, type PersonWindow } from "../../screens/PersonScreen/PersonScreen";
import { getJson } from "../client";
import type { Schemas } from "../types";
import { delayedSince, useLive, useRefresh } from "./useLive";
import { TYPING_MS, useDebounced } from "./useDebounced";
import { useSlow } from "./useSlow";
import { useRequestDrawer } from "./useRequestDrawer";

const HOUR = 3_600_000;
const WINDOW_MS: Record<PersonWindow, number | null> = { "24h": 24 * HOUR, "7d": 7 * 24 * HOUR, "14d": 14 * 24 * HOUR, all: null };
const PAGE_SIZE = 20;

/** `?from&to…` for a query; a missing end is now. */
function qs(params: Record<string, string | number | string[] | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === "") continue;
    if (Array.isArray(v)) for (const x of v) p.append(k, x);
    else p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function usePersonPage(personId: string): PersonScreenProps {
  const [url, setUrl] = useUrlState();
  const live = useLive();
  const win: PersonWindow = (WINDOWS as readonly string[]).includes(url.win ?? "") ? (url.win as PersonWindow) : "14d";
  const range = url.from && url.to ? { from: url.from, to: url.to } : null;
  const tab: PersonTab = url.tab === "requests" ? "requests" : "sessions";
  // The window's start, computed when a query runs (so its key stays stable while now moves).
  const windowFrom = () => (WINDOW_MS[win] === null ? undefined : new Date(Date.now() - (WINDOW_MS[win] ?? 0)).toISOString());
  const scoped = () => (range ? { from: range.from, to: range.to } : { from: windowFrom() });
  const base = `/people/${encodeURIComponent(personId)}`;
  const poll = { refetchInterval: live.refetchInterval, retry: false } as const;
  const typedQ = useDebounced(url.q, TYPING_MS);
  // The agent scope (§11M 3.3): one agent from `agent=`, else all. In All agents the chips are the
  // shared channels; the API filters by its own kinds, so a shared chip asks for its members.
  const scope = (url.agent[0] ?? "all") as AgentScopeValue;
  const agentParam = scope === "all" ? undefined : [scope];
  const kindParam = url.kind.flatMap((k) => SHARED_CHANNELS.find((c) => c.channel === k)?.members ?? [k]);

  const personQ = useQuery({ queryKey: ["person", personId], queryFn: ({ signal }) => getJson<Schemas["PersonResponse"]>(base, signal), ...poll });
  // The bin from the track's width (§11G.A5); `All` has no known span, so the server picks.
  const trackPx = useTrackPx();
  const span = WINDOW_MS[win];
  const bin = span ? pickBin(span, trackPx) : undefined;
  const timelineQ = useQuery({
    queryKey: ["person", personId, "timeline", win, bin],
    queryFn: ({ signal }) => getJson<Schemas["PersonTimelineResponse"]>(`${base}/timeline${qs({ from: windowFrom(), bin })}`, signal),
    placeholderData: keepPreviousData,
    ...poll,
  });
  const summaryQ = useQuery({
    queryKey: ["person", personId, "summary", win, range],
    queryFn: ({ signal }) => getJson<Schemas["PersonSummaryResponse"]>(`${base}/summary${qs(scoped())}`, signal),
    placeholderData: keepPreviousData,
    ...poll,
  });
  const sessionsQ = useQuery({
    queryKey: ["person", personId, "sessions", win, range, scope, tab === "sessions" ? url.page : 1],
    queryFn: ({ signal }) =>
      getJson<Schemas["PersonSessionsResponse"]>(`${base}/sessions${qs({ ...scoped(), agent: agentParam, page: tab === "sessions" ? url.page : 1, pageSize: PAGE_SIZE })}`, signal),
    placeholderData: keepPreviousData,
    ...poll,
  });
  const requestsQ = useQuery({
    queryKey: ["person", personId, "requests", win, range, scope, url.kind, typedQ, url.captured, url.hidden, tab === "requests" ? url.page : 1],
    queryFn: ({ signal }) =>
      getJson<Schemas["PersonRequestsResponse"]>(
        `${base}/requests${qs({ ...scoped(), agent: agentParam, kind: kindParam, q: typedQ, captured: url.captured, hidden: url.hidden ? "true" : undefined, page: tab === "requests" ? url.page : 1, pageSize: PAGE_SIZE })}`,
        signal,
      ),
    placeholderData: keepPreviousData,
    ...poll,
  });

  // The drawer opens from a row: the row seeds its header until the request loads.
  const [seed, setSeed] = useState<Seed | undefined>();
  const dtab: DrawerTab = url.dtab === "raw" || url.dtab === "all" ? url.dtab : "summary";
  const drawer = useRequestDrawer(url.req ?? "", dtab, !!url.req);

  const aboutProducts = useAboutProducts(url.about);
  const refresh = useRefresh([personQ, timelineQ, summaryQ, sessionsQ, requestsQ]);
  const slow = {
    person: useSlow(personQ.isFetching, ["person", personId], true),
    summary: useSlow(summaryQ.isFetching, ["person", personId, "summary"]),
    timeline: useSlow(timelineQ.isFetching, ["person", personId, "timeline"]),
    sessions: useSlow(sessionsQ.isFetching, ["person", personId, "sessions"]),
    requests: useSlow(requestsQ.isFetching, ["person", personId, "requests"]),
  };
  const person = personQ.data?.data;
  const delayed = delayedSince(personQ.data?.meta, !!person?.live);
  const status = live.status;
  const now = personQ.data?.meta.asOf ?? status?.meta.asOf ?? new Date().toISOString();

  return {
    now,
    live: live.badge,
    paused: live.paused,
    onPause: live.togglePause,
    window: win,
    onWindow: (w) => setUrl({ win: w === "14d" ? undefined : w, from: undefined, to: undefined }),
    banner: refresh.failed && refresh.since
      ? { kind: "refreshFailed", since: refresh.since, retryInS: refresh.retryInS, onRetry: refresh.retry, retrying: refresh.retrying }
      : delayed
        ? { kind: "delayed", since: delayed }
        : null,
    waiting: status && status.data.firstRequestAt === null ? { steps: waitingSteps(status.data, new Date(status.meta.asOf)), checkedAt: status.meta.asOf } : null,
    person: { ...toRegion({ ...personQ, data: person }), slow: slow.person },
    summary: { ...toRegion({ ...summaryQ, data: summaryQ.data?.data }), slow: slow.summary },
    timeline: { ...toRegion({ ...timelineQ, data: timelineQ.data?.data }), slow: slow.timeline },
    range,
    onRange: (r) => setUrl({ from: r?.from, to: r?.to }),
    tab,
    onTab: (t) => setUrl({ tab: t === "sessions" ? undefined : t }),
    sessions: { ...toRegion(sessionsQ, (r) => r.data.length === 0), slow: slow.sessions },
    requests: { ...toRegion(requestsQ, (r) => r.data.length === 0), slow: slow.requests },
    page: url.page,
    onPage: (page) => setUrl({ page }),
    q: url.q ?? "",
    onQ: (q) => setUrl({ q }, { replace: true }),
    kinds: url.kind,
    onKinds: (kind) => setUrl({ kind }),
    agentScope: scope,
    // A new scope's chips differ: the kind filter resets with it.
    onAgentScope: (a) => setUrl({ agent: a === "all" ? [] : [a], kind: [] }),
    showHidden: url.hidden,
    onShowHidden: (shown) => setUrl({ hidden: shown }),
    notCaptured: url.captured === "false",
    onNotCaptured: (on) => setUrl(on ? { tab: "requests", captured: "false" } : { captured: undefined }),
    about: aboutState(url.about, setUrl, status?.data, aboutProducts),
    onRetry: (r) => void { person: personQ, summary: summaryQ, timeline: timelineQ, sessions: sessionsQ, requests: requestsQ }[r].refetch(),
    peopleHref: "/people",
    sessionHref: (id) => `/sessions/${encodeURIComponent(id)}`,
    onOpenRequest: (row) => {
      setSeed(row);
      setUrl({ req: row.requestId, dtab: undefined });
    },
    drawer: url.req
      ? {
          requestId: url.req,
          seed: seed?.requestId === url.req ? seed : requestsQ.data?.data.find((r) => r.requestId === url.req),
          detail: drawer.detail,
          raw: drawer.raw,
          details: drawer.details,
          onRetry: (part) => drawer.retry[part](),
          tab: dtab,
          onTab: (t) => setUrl({ dtab: t === "summary" ? undefined : t }, { replace: true }),
          onClose: () => setUrl({ req: undefined, dtab: undefined }),
          onNavigate: (id) => setUrl({ req: id }, { replace: true }),
          onRawHover: drawer.prefetchRaw,
          curl: drawer.curl,
          link: typeof window === "undefined" ? undefined : window.location.href,
        }
      : null,
  };
}
