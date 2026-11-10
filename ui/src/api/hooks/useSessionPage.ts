// The session page's data and URL state (handoff §7.3), shaped as SessionScreen's props.
import { pickBin, useTrackPx } from "../../lib/bins";
import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import type { RequestKind } from "../../components/RequestRow/RequestRow";
import type { DrawerTab } from "../../components/RequestDrawer/DrawerTabs";
import type { Range } from "../../components/Timeline/Brush";
import { toRegion } from "../../lib/region";
import { aboutState, useAboutProducts } from "./aboutState";
import { useUrlState } from "../../pages/useUrlState";
import { PAGE_SIZE, type SessionScreenProps, type SessionTab } from "../../screens/SessionScreen/SessionScreen";
import { getJson } from "../client";
import type { Schemas, SessionTimeline } from "../types";
import { delayedSince, useLive, useRefresh } from "./useLive";
import { TYPING_MS, useDebounced } from "./useDebounced";
import { useSlow } from "./useSlow";
import { useRequestDrawer } from "./useRequestDrawer";

/** Live sessions follow the last 15 min (handoff §7.3). */
/** Range-scoped queries wait this long after the brush settles (§7.3). */
const DEBOUNCE_MS = 200;
const TABS: SessionTab[] = ["prompts", "requests", "events"];


const qs = (params: Record<string, string | number | boolean | string[] | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === "") continue;
    if (Array.isArray(v)) v.forEach((x) => p.append(k, x));
    else p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
};

/**
 * Everything SessionScreen needs: the header and whole-session timeline (polled
 * while live), the range from the URL (or the whole session),
 * the range-scoped regions debounced 200 ms after the brush settles, the drawer
 * from `?req`, and the expanded events' JSON.
 */
/** The Events tab's filters as query parameters. Client internals are hidden unless
 * asked for (the API's default), so showing them is the one that needs a parameter. */
export function eventFilterParams(q: string | undefined, internal: boolean, ticks: boolean) {
  return { q, internal: internal ? true : undefined, collapseTicks: ticks };
}

export function useSessionPage(sessionId: string): SessionScreenProps {
  const [url, setUrl] = useUrlState();
  // Each jump to a prompt, so the screen scrolls to it once (and again on a repeat click).
  const [jumpTo, setJumpTo] = useState<SessionScreenProps["jumpTo"]>(() => (url.prompt ? { index: Number(url.prompt), seq: 0 } : null));
  const live = useLive();
  const base = `/sessions/${encodeURIComponent(sessionId)}`;

  const headerQ = useQuery({
    queryKey: ["session", sessionId],
    queryFn: ({ signal }) => getJson<Schemas["SessionResponse"]>(base, signal),
    refetchInterval: (q) => (q.state.data?.data.live ? live.refetchInterval(q) : false),
  });
  const header = headerQ.data?.data;
  const isLive = !!header?.live;
  // The bin from the track's width and the session's span (§11G.A5), once the header gives the span.
  const trackPx = useTrackPx();
  const spanMs = header ? Date.parse(header.end ?? headerQ.data?.meta.asOf ?? header.start) - Date.parse(header.start) : null;
  const bin = spanMs !== null ? pickBin(spanMs, trackPx) : undefined;
  const timelineQ = useQuery({
    queryKey: ["session", sessionId, "timeline", bin],
    queryFn: ({ signal }) => getJson<Schemas["SessionTimelineResponse"]>(`${base}/timeline${qs({ bin })}`, signal),
    enabled: bin !== undefined,
    refetchInterval: (q) => (isLive ? live.refetchInterval(q) : false),
    placeholderData: keepPreviousData,
  });
  const personId = header?.personId ?? null;
  const personQ = useQuery({
    queryKey: ["person", personId],
    queryFn: ({ signal }) => getJson<Schemas["PersonResponse"]>(`/people/${encodeURIComponent(personId ?? "")}`, signal),
    enabled: personId !== null,
  });

  // The range: brushed (in the URL), else the whole session, live or not (it opens on the
  // whole session, and clearing a range returns to it: no implicit last-15-min window).
  const asOf = timelineQ.data?.data.now ?? headerQ.data?.meta.asOf ?? new Date().toISOString();
  const brushed = url.from && url.to ? { from: url.from, to: url.to } : null;
  const rangeMode: SessionScreenProps["rangeMode"] = brushed ? "range" : "whole";
  const range: Range = brushed ?? { from: header?.start ?? asOf, to: (isLive ? asOf : header?.end) ?? asOf };
  // The whole session sends no range (the server's default is the session, up to now while live).
  const typedQ = useDebounced(url.q, TYPING_MS);
  // Debounced as strings: a new object each render would restart the timer forever.
  const scopedFrom = useDebounced(brushed?.from ?? "", DEBOUNCE_MS);
  const scopedTo = useDebounced(brushed?.to ?? "", DEBOUNCE_MS);
  const scoped = useMemo(() => (scopedFrom && scopedTo ? { from: scopedFrom, to: scopedTo } : {}), [scopedFrom, scopedTo]);
  const tab: SessionTab = TABS.includes(url.tab as SessionTab) ? (url.tab as SessionTab) : "prompts";
  const kinds = url.kind as RequestKind[];

  const scopedQuery = <T,>(name: string, path: string, params: Record<string, string | number | boolean | string[] | undefined>, enabled = true) => ({
    queryKey: ["session", sessionId, name, scoped, params],
    queryFn: ({ signal }: { signal: AbortSignal }) => getJson<T>(`${base}/${path}${qs({ ...scoped, ...params })}`, signal),
    placeholderData: (prev: T | undefined) => prev,
    refetchInterval: (q: Parameters<typeof live.refetchInterval>[0]) => (isLive ? live.refetchInterval(q) : false),
    enabled,
  });
  const stateQ = useQuery(scopedQuery<Schemas["SessionStateResponse"]>("state", "state", {}));
  const pageParams = { page: url.page, pageSize: PAGE_SIZE };
  // A jump (`prompt`) asks for the page that holds that prompt.
  const promptsQ = useQuery(
    scopedQuery<Schemas["SessionPromptsResponse"]>("prompts", "prompts", url.prompt ? { pageSize: PAGE_SIZE, prompt: url.prompt } : pageParams, tab === "prompts"),
  );
  const requestsQ = useQuery(scopedQuery<Schemas["SessionRequestsResponse"]>("requests", "requests", { ...pageParams, kind: kinds, q: typedQ, captured: url.captured, hidden: url.hidden || undefined }, tab === "requests"));
  const eventsQ = useQuery(
    scopedQuery<Schemas["SessionEventsResponse"]>("events", "events", { ...pageParams, ...eventFilterParams(typedQ, url.internal, url.ticks) }, tab === "events"),
  );

  const slow = {
    session: useSlow(headerQ.isFetching, ["session", sessionId], true),
    timeline: useSlow(timelineQ.isFetching, ["session", sessionId, "timeline"]),
    state: useSlow(stateQ.isFetching, ["session", sessionId, "state"]),
    prompts: useSlow(promptsQ.isFetching, ["session", sessionId, "prompts"]),
    requests: useSlow(requestsQ.isFetching, ["session", sessionId, "requests"]),
    events: useSlow(eventsQ.isFetching, ["session", sessionId, "events"]),
  };

  // Expanded events load their full JSON (§6A.4).
  const [openEvents, setOpenEvents] = useState<string[]>([]);
  const eventQs = useQueries({
    queries: openEvents.map((id) => ({
      queryKey: ["event", id],
      queryFn: ({ signal }: { signal: AbortSignal }) => getJson<Schemas["EventResponse"]>(`/events/${encodeURIComponent(id)}`, signal),
    })),
  });

  // The drawer: `?req`, with the clicked row's data as its header until the request lands.
  const dtab = (["summary", "raw", "all"].includes(url.dtab ?? "") ? url.dtab : "summary") as DrawerTab;
  const drawerQ = useRequestDrawer(url.req ?? "", dtab, url.req !== undefined);
  const seeds = useRef(new Map<string, Schemas["RequestRow"]>());

  const aboutProducts = useAboutProducts(url.about);
  const refresh = useRefresh([headerQ, timelineQ, stateQ, promptsQ, requestsQ, eventsQ]);
  const delayed = delayedSince(headerQ.data?.meta, isLive);
  const width = Date.parse(range.to) - Date.parse(range.from);
  const shift = (dir: 1 | -1) => {
    const start = header ? Date.parse(header.start) : -Infinity;
    const end = Date.parse(header?.end ?? asOf);
    const from = Math.max(start, Math.min(end - width, Date.parse(range.from) + dir * width));
    setUrl({ from: new Date(from).toISOString(), to: new Date(from + width).toISOString() });
  };
  const retry = {
    session: headerQ.refetch,
    timeline: timelineQ.refetch,
    state: stateQ.refetch,
    prompts: promptsQ.refetch,
    requests: requestsQ.refetch,
    events: eventsQ.refetch,
  };
  const eventJson = useMemo(
    () => Object.fromEntries(openEvents.map((id, i) => [id, toRegion({ ...(eventQs[i] ?? { data: undefined, isPending: true, isFetching: true, isError: false, error: null }), data: eventQs[i]?.data?.data })])),
    [openEvents, eventQs],
  );

  return {
    sessionId,
    about: aboutState(url.about, setUrl, live.status?.data, aboutProducts),
    session: { ...toRegion({ ...headerQ, data: header }), slow: slow.session },
    person: personId
      ? { name: personQ.data?.data.name ?? null, href: `/people/${encodeURIComponent(personId)}`, agents: personQ.data?.data.agentInfo.map((a) => a.agent) }
      : null,
    timeline: { ...toRegion<SessionTimeline>({ ...timelineQ, data: timelineQ.data?.data }), slow: slow.timeline },
    state: { ...toRegion({ ...stateQ, data: stateQ.data?.data }), slow: slow.state },
    prompts: { ...toRegion(promptsQ, (r) => r.data.length === 0), slow: slow.prompts },
    requests: { ...toRegion(requestsQ, (r) => r.data.length === 0), slow: slow.requests },
    events: { ...toRegion(eventsQ, (r) => r.data.length === 0), slow: slow.events },
    onRetry: (r) => void retry[r](),
    live: live.badge,
    paused: live.paused,
    onPause: live.togglePause,
    banner: refresh.failed && refresh.since
      ? { kind: "refreshFailed", since: refresh.since, retryInS: refresh.retryInS, onRetry: refresh.retry, retrying: refresh.retrying }
      : delayed
        ? { kind: "delayed", since: delayed }
        : null,
    link: typeof window === "undefined" ? "" : window.location.href,
    rangeMode,
    range,
    onRange: (r) => setUrl({ from: r?.from, to: r?.to }),
    onPrevRange: () => shift(-1),
    onNextRange: () => shift(1),
    tab,
    onTab: (t) => setUrl({ tab: t === "prompts" ? undefined : t, q: undefined }),
    jumpTo,
    // To the Prompts tab on that prompt's page, dropping a range that excludes it.
    onJumpToPrompt: (index, t) => {
      const outside = !!brushed && !!t && (Date.parse(t) < Date.parse(brushed.from) || Date.parse(t) > Date.parse(brushed.to));
      setUrl({ tab: undefined, prompt: String(index), ...(outside ? { from: undefined, to: undefined } : {}) });
      setJumpTo((j) => ({ index, seq: (j?.seq ?? 0) + 1 }));
    },
    page: url.page,
    onPage: (page) => setUrl({ page }),
    requestFilter: { q: url.q ?? "", kinds, notCaptured: url.captured === "false", hidden: url.hidden },
    // Turning the not-captured filter on (from "+N not captured") switches to Requests, as its own history entry.
    onRequestFilter: (f) =>
      f.notCaptured && url.captured !== "false"
        ? setUrl({ tab: "requests", q: f.q || undefined, kind: f.kinds, captured: "false" })
        : setUrl({ q: f.q, kind: f.kinds, captured: f.notCaptured ? "false" : undefined, hidden: !!f.hidden }, { replace: true }),
    onShowHiddenRequests: () => setUrl({ tab: "requests", hidden: true }),
    eventFilter: { q: url.q ?? "", internal: url.internal, ticks: url.ticks },
    onEventFilter: (f) => setUrl({ q: f.q, internal: f.internal, ticks: f.ticks }, { replace: true }),
    onOpenRequest: (id, seed) => {
      if (seed) seeds.current.set(id, seed);
      setUrl({ req: id, dtab: undefined });
    },
    drawer: url.req
      ? {
          requestId: url.req,
          seed: seeds.current.get(url.req),
          detail: drawerQ.detail,
          raw: drawerQ.raw,
          details: drawerQ.details,
          onRetry: (part) => drawerQ.retry[part](),
          tab: dtab,
          onTab: (t) => setUrl({ dtab: t === "summary" ? undefined : t }, { replace: true }),
          onNavigate: (id) => setUrl({ req: id }, { replace: true }),
          onRawHover: drawerQ.prefetchRaw,
          curl: drawerQ.curl,
          link: typeof window === "undefined" ? "" : window.location.href,
        }
      : null,
    onCloseDrawer: () => setUrl({ req: undefined, dtab: undefined }),
    eventJson,
    onToggleEvent: (id) => setOpenEvents((o) => (o.includes(id) ? o.filter((x) => x !== id) : [...o, id])),
    onRetryEvent: (id) => void eventQs[openEvents.indexOf(id)]?.refetch(),
  };
}
