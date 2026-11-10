import { AboutThisData, type AboutState } from "../../components/AboutThisData/AboutThisData";
import { useMemo, useState, type ReactNode } from "react";
import type { Schemas } from "../../api/types";
import { anyReports, isNested, reports, threadFacts } from "../../lib/agents";
import { formatCount } from "../../lib/format";
import { useLatest } from "../../lib/useLatest";
import { slowNotice, type Region } from "../../lib/region";
import { formatDayTime, formatRange } from "../../lib/time";
import { AppBar } from "../../components/AppBar/AppBar";
import { Banner, type BannerProps } from "../../components/Banner/Banner";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import { CoverageLine } from "../../components/CoverageLine/CoverageLine";
import { EmptyState } from "../../components/EmptyState/EmptyState";
import { FilterChip } from "../../components/FilterChip/FilterChip";
import { KeyValueList } from "../../components/KeyValueList/KeyValueList";
import type { LiveBadgeProps } from "../../components/LiveBadge/LiveBadge";
import { PageHeader } from "../../components/PageHeader/PageHeader";
import { PersonHeader as PersonHeaderBox } from "../../components/PersonHeader/PersonHeader";
import { Pagination } from "../../components/Pagination/Pagination";
import { RegionState } from "../../components/RegionState/RegionState";
import { RequestDrawer, type RequestDrawerProps } from "../../components/RequestDrawer/RequestDrawer";
import { RequestRow, type Request } from "../../components/RequestRow/RequestRow";
import { RequestFilters, CODEX_CHANNELS, type RequestChannel } from "../../components/RequestFilters/RequestFilters";
import { RequestTable } from "../../components/RequestTable/RequestTable";
import { SessionRow } from "../../components/SessionRow/SessionRow";
import { SessionTable, type SessionTableView } from "../../components/SessionTable/SessionTable";
import { AgentScope, type AgentScopeValue } from "../../components/AgentScope/AgentScope";
import type { Agent } from "../../components/AgentChip/AgentChip";
import type { Channel } from "../../components/ChannelSwatch/ChannelSwatch";
import { requestDetail, sharedChannel, sharedRequestChannels } from "../SessionScreen/sessionView";
import { Skeleton } from "../../components/Skeleton/Skeleton";
import { StatRibbon } from "../../components/StatRibbon/StatRibbon";
import { Tabs } from "../../components/Tabs/Tabs";
import { TextInput } from "../../components/TextInput/TextInput";
import { BandTrack } from "../../components/Timeline/BandTrack";
import { BarTrack, gapRowLabel } from "../../components/Timeline/BarTrack";
import { FrictionTrack, frictionRowLabels } from "../../components/Timeline/FrictionTrack";
import type { Range } from "../../components/Timeline/Brush";
import { DensityTrack } from "../../components/Timeline/DensityTrack";
import { laneInteractions, type TimelineActions } from "../../components/Timeline/interactions";
import { Lane, LANE_HEIGHT } from "../../components/Timeline/Lane";
import { MarkerTrack } from "../../components/Timeline/MarkerTrack";
import { RowTrack } from "../../components/Timeline/RowTrack";
import { perBin } from "../../components/Timeline/sessionLanes";
import { Timeline } from "../../components/Timeline/Timeline";
import { LANE_DEFINITIONS, nowTip } from "../../components/Timeline/timelineTips";
import { WaitingChecklist, type WaitingStep } from "../../components/WaitingChecklist/WaitingChecklist";
import {
  capabilityRows,
  footprintRows,
  nearest,
  personHeaderProps,
  personLanes,
  postureChips,
  ribbonTiles,
  windowLabel,
  type PersonHeader,
  type PersonSummary,
  type PersonTimeline,
} from "./personView";
import "./PersonScreen.css";

export type PersonTab = "sessions" | "requests";
export const WINDOWS = ["24h", "7d", "14d", "all"] as const;
export type PersonWindow = (typeof WINDOWS)[number];
export const REQUEST_KINDS = ["model", "analytics", "flags", "count", "account"] as const;

/** The person's agents, in order (the scope control's options; two or more show it). */
const personAgents = (p: Pick<PersonScreenProps, "person">): Agent[] => p.person.data?.agentInfo.map((a) => a.agent) ?? [];

/** Each agent's own request channels (§11L.6), for All requests scoped to one agent (§11M 3.3). */
const AGENT_CHANNELS: Partial<Record<Agent, { channel: Channel; label: string }[]>> = {
  "claude-code": [
    { channel: "model", label: "model" },
    { channel: "analytics", label: "analytics" },
    { channel: "flags", label: "flags" },
    { channel: "count", label: "count" },
    { channel: "account", label: "account" },
  ],
  codex: CODEX_CHANNELS,
};

export type PersonScreenProps = {
  /** "About this data": the AppBar button and the drawer (`?about=1`). */
  about?: AboutState;
  /** The reference "now" (the API's asOf), for times and the now line. */
  now: string;
  live: LiveBadgeProps;
  paused: boolean;
  onPause: () => void;
  window: PersonWindow;
  onWindow: (w: PersonWindow) => void;
  /** Delayed or refresh failed (handoff §5.3); the page decides which. */
  banner?: Omit<BannerProps, "now" | "page"> | null;
  /** First run, before any traffic: the page shows only the checklist (§7.2 waiting). */
  waiting?: { steps: WaitingStep[]; checkedAt: string } | null;
  person: Region<PersonHeader>;
  summary: Region<PersonSummary>;
  timeline: Region<PersonTimeline>;
  /** The brush range; it filters both tabs and the ribbon and footprint. */
  range: Range | null;
  onRange: (r: Range | null) => void;
  tab: PersonTab;
  onTab: (t: PersonTab) => void;
  sessions: Region<Schemas["PersonSessionsResponse"]>;
  requests: Region<Schemas["PersonRequestsResponse"]>;
  page: number;
  onPage: (p: number) => void;
  /** All requests filters: free text and kinds (none = all). */
  q: string;
  onQ: (q: string) => void;
  kinds: string[];
  onKinds: (k: string[]) => void;
  /** The agent scope (§11M 3.3): all the person's agents, or one; it scopes Sessions and All requests. */
  agentScope?: AgentScopeValue;
  onAgentScope?: (a: AgentScopeValue) => void;
  /** All requests includes hidden calls (Codex's prewarm and title calls; the API counts them as `hiddenCount`). */
  showHidden?: boolean;
  onShowHidden?: (shown: boolean) => void;
  /** All requests shows only calls the proxy didn't capture (`captured=false`, handoff §11E.2). */
  notCaptured: boolean;
  /** Turns that filter on (switching to All requests) or off. */
  onNotCaptured: (on: boolean) => void;
  onRetry: (region: "person" | "summary" | "timeline" | "sessions" | "requests") => void;
  peopleHref: string;
  sessionHref: (sessionId: string) => string;
  /** Click a request row: the drawer opens in place (`?req`). */
  onOpenRequest: (row: Request) => void;
  /** The open drawer, when `?req` is set (its Regions come from useRequestDrawer). */
  drawer?: RequestDrawerProps | null;
};

/** The ribbon's tiles while it loads, at their final size. */
const RIBBON_LABELS = ["Model calls", "Analytics", "Account calls", "Reach", "Tokens out", "Cost est."];

const err = (r: Region<unknown>, onRetry: () => void) => r.error && { message: r.error.message, queryId: r.error.queryId, onRetry };

/** A panel-sized skeleton for a region's first load. */
const Block = ({ height }: { height: number }) => <div className="person-skeleton-block border border-line bg-surface" style={{ height }} aria-hidden />;

/**
 * The person page (handoff §7.2, Main.dc.html): app bar, banner, header and
 * posture, the ribbon with coverage, the activity timeline (its brush filters
 * everything below), capabilities and footprint, then Sessions and All requests.
 * A request row opens the drawer in place. Pure: every region arrives as a
 * Region from the page.
 */
export function PersonScreen(p: PersonScreenProps) {
  const at = new Date(p.now);
  const person = p.person.data;
  // The traffic no account appears on (§11G.C3) is a person-like page of its own.
  const unattributed = person?.personId === "unattributed";
  const name = person ? (unattributed ? "Unattributed traffic" : (person.name ?? "name unknown")) : null;
  const appBar = (
    <>
      <AppBar
        breadcrumb={[{ label: "people", href: p.peopleHref }, { label: unattributed ? "unattributed" : name, agents: unattributed ? undefined : person?.agentInfo.map((a) => a.agent) }]}
        window={{
          options: WINDOWS.map((w) => ({ value: w, label: w === "all" ? "All" : w })),
          value: p.window,
          onChange: (w) => p.onWindow(w as PersonWindow),
          "aria-label": "Time window",
        }}
        live={p.live}
        paused={p.paused}
        onPause={p.onPause}
        loading={p.person.status === "loading"}
        onAbout={p.about?.onOpen}
      />
      {p.about?.open && <AboutThisData contentRetained={p.about.contentRetained} products={p.about.products} onClose={p.about.onClose} />}
    </>
  );

  if (p.waiting) {
    return (
      <div className="min-h-screen bg-bg text-ink">
        {appBar}
        <main className="person-waiting page-enter">
          <div>
            <h1 className="type-title text-ink">Waiting for traffic</h1>
            <p className="type-body text-ink-muted">Nothing from Claude Code has reached the proxy yet. This page fills in on its own when the first request arrives.</p>
          </div>
          <WaitingChecklist steps={p.waiting.steps} checkedAt={p.waiting.checkedAt} />
        </main>
      </div>
    );
  }

  const sessionsTotal = p.sessions.data?.meta.total ?? p.summary.data?.ribbon.sessions;
  const requestsTotal = p.requests.data?.meta.total;
  const rangeLabel = p.range ? formatRange(p.range.from, p.range.to, at) : null;
  const t = p.timeline.data;

  // Empty range: the brush covers no activity; offer the nearest activity either side.
  const emptyRange = !!p.range && p.sessions.status !== "loading" && p.sessions.data?.data.length === 0 && (p.requests.data?.data.length ?? 0) === 0;
  const near = p.range ? nearest(t, p.range.from, p.range.to) : {};
  const posture = person && !unattributed ? postureChips(person) : [];

  return (
    <div className="min-h-screen bg-bg text-ink">
      {appBar}
      {p.banner && <Banner {...p.banner} now={at} page />}
      <main className="person-page page-enter">
        <RegionState status={p.person.status} slow={slowNotice(p.person, "this person")} hasData={!!person} skeleton={<Skeleton shape="block" />} error={err(p.person, () => p.onRetry("person"))} label="this person">
          {person &&
            (unattributed ? (
              <div className="person-header">
                <PageHeader
                  title={name ?? ""}
                  subtitle={`${t ? formatCount(t.sessions.length) : "…"} sessions with no account or device identity${t ? ` · ${windowLabelOf(t)}` : ""}`}
                />
              </div>
            ) : (
              <div className="person-header-stack">
                <PersonHeaderBox {...personHeaderProps(person, p.summary.data, at)} />
                {posture.length > 0 && (
                  <div className="person-posture">
                    <span className="type-key text-ink-faint">Posture</span>
                    {posture.map((c) => (
                      <Chip key={c} tone="muted">
                        {c}
                      </Chip>
                    ))}
                  </div>
                )}
              </div>
            ))}
        </RegionState>

        <RegionState status={p.summary.status} slow={slowNotice(p.summary, "the summary")} hasData={!!p.summary.data} skeleton={<StatRibbon tiles={RIBBON_LABELS.map((label) => ({ label, value: null }))} loading />} error={err(p.summary, () => p.onRetry("summary"))} label="the summary">
          {p.summary.data && (
            <div className="person-ribbon border border-line bg-surface">
              <StatRibbon tiles={ribbonTiles(p.summary.data)} />
              <CoverageLine coverage={p.summary.data.coverage} now={at} onView={() => p.onNotCaptured(true)} />
            </div>
          )}
        </RegionState>

        <RegionState status={p.timeline.status} slow={slowNotice(p.timeline, "the timeline")} hasData={!!t} skeleton={<Block height={320} />} error={err(p.timeline, () => p.onRetry("timeline"))} label="the timeline">
          {t && <PersonTimelinePanel d={t} presence={!person || unattributed || anyReports(person.capabilities, person.agentInfo.map((a) => a.agent), "presence")} range={p.range} onRange={p.onRange} sessionHref={p.sessionHref} onNotCaptured={() => p.onNotCaptured(true)} />}
        </RegionState>

        <div className="person-panels">
          <RegionState status={p.summary.status} slow={slowNotice(p.summary, "capabilities")} hasData={!!p.summary.data} skeleton={<KeyValueList title="Capabilities · seen in any session" rows={[]} loading />} label="capabilities">
            {p.summary.data && <KeyValueList title="Capabilities · seen in any session" accent="skills" rows={capabilityRows(p.summary.data)} />}
          </RegionState>
          <RegionState status={p.summary.status} slow={slowNotice(p.summary, "footprint")} hasData={!!p.summary.data} skeleton={<KeyValueList title="Footprint" rows={[]} loading />} label="footprint">
            {p.summary.data && <KeyValueList title={rangeLabel ? `Footprint · ${rangeLabel}` : "Footprint · all sessions"} rows={footprintRows(p.summary.data)} />}
          </RegionState>
        </div>

        <section aria-label="Sessions and requests">
          <div className="flex flex-wrap items-center gap-3">
            <Tabs
              label="Person tables"
              value={p.tab}
              onChange={(id) => p.onTab(id as PersonTab)}
              tabs={[
                { id: "sessions", label: "Sessions", count: sessionsTotal ?? undefined },
                { id: "requests", label: "All requests", count: requestsTotal ?? undefined },
              ]}
            />
            {/* The agent scope, only for a person with more than one agent (§11M 3.3). */}
            {p.onAgentScope && <AgentScope agents={personAgents(p)} value={p.agentScope ?? "all"} onChange={p.onAgentScope} />}
            {rangeLabel && (
              <Chip tone="range" tip={{ body: "The range filters both tabs.", hint: "× or Esc clears it" }}>
                {rangeLabel}
              </Chip>
            )}
          </div>
          {emptyRange && p.range ? (
            <EmptyState
              chip={<Chip tone="range">{rangeLabel}</Chip>}
              title="No Claude Code activity in this range"
              body="No sessions were open and no requests from this person reached the proxy. Activity that went around the proxy would still show up later as “reported, not captured”."
              actions={
                <>
                  <Button onClick={() => p.onRange(null)}>Clear range</Button>
                  {near.earlier && <Button onClick={() => p.onRange(around(near.earlier as string, p.range as Range))}>{`← ${formatDayTime(near.earlier)} · nearest earlier`}</Button>}
                  {near.later && <Button onClick={() => p.onRange(around(near.later as string, p.range as Range))}>{`${formatDayTime(near.later)} · nearest later →`}</Button>}
                </>
              }
            />
          ) : p.tab === "sessions" ? (
            <SessionsTable {...p} at={at} />
          ) : (
            <RequestsTable {...p} at={at} />
          )}
        </section>
      </main>
      {p.drawer && <RequestDrawer {...p.drawer} />}
    </div>
  );
}

/** A range of the same length centred on `t` (jump to the nearest activity). */
function around(t: string, r: Range): Range {
  const half = (Date.parse(r.to) - Date.parse(r.from)) / 2;
  const c = Date.parse(t);
  return { from: new Date(c - half).toISOString(), to: new Date(c + half).toISOString() };
}

function PersonTimelinePanel({ d, presence, range, onRange, sessionHref, onNotCaptured }: { d: PersonTimeline; presence: boolean; range: Range | null; onRange: (r: Range | null) => void; sessionHref: (id: string) => string; onNotCaptured: () => void }) {
  // The lanes are thousands of marks: built once per timeline payload, so a page
  // re-render (a poll's fetching flag) or the brush moving doesn't redo them.
  const l = useMemo(() => personLanes(d), [d]);
  const cb = useLatest({ onRange, sessionHref, onNotCaptured });
  const per = perBin(d.binSeconds);
  const header: ReactNode = (
    <>
      <span className="type-key text-ink-faint">Activity · {windowLabelOf(d)}</span>
      <span className="type-data text-ink-muted">{`${formatCount(d.sessions.length)} ${d.sessions.length === 1 ? "session" : "sessions"} · ${binWord(d.binSeconds)}`}</span>
      <span className="flex-1" />
      <span className="type-data text-ink-faint">drag to filter sessions and requests below</span>
    </>
  );
  const lanes = useMemo(() => {
    const actions: TimelineActions = {
      select: (r) => cb.current.onRange(r),
      openSession: (id) => window.location.assign(cb.current.sessionHref(id)),
      listNotCaptured: (r) => {
        cb.current.onRange(r);
        cb.current.onNotCaptured();
      },
    };
    const on = laneInteractions(d, actions);
    return (
      <>
      <Lane label="Sessions" height={l.rows > 1 ? 6 + l.rows * 34 : 44} summary={`${l.bands.length} sessions`} sunken>
        <BandTrack bands={l.bands} now={d.now ?? d.to} interact={on.bands} />
      </Lane>
      <Lane label="Prompts" height={26} summary={`${l.prompts.length} prompts`}>
        <MarkerTrack markers={l.prompts} interact={on.prompts} />
      </Lane>
      <Lane label="Model calls" sublabel={per} height={LANE_HEIGHT.barsWithGapRow} summary={`${l.gaps.length} gaps`} definition={LANE_DEFINITIONS.modelCalls} footer={gapRowLabel(l.gaps)}>
        <BarTrack bins={l.callBins} binSeconds={d.binSeconds} color="model" gaps={l.gaps} gapRow interact={on.modelCalls} gapTip={on.gapTip} onGap={on.onGap} />
      </Lane>
      <Lane label="Analytics" sublabel="events" height={44} summary="analytics events" definition={LANE_DEFINITIONS.analytics}>
        <DensityTrack bins={l.analyticsBins} binSeconds={d.binSeconds} color="analytics" interact={on.analytics} />
      </Lane>
      {/* Presence is Claude Code's (§11L field audit): absent when none of the person's agents reports it. */}
      {presence && (
        <Lane label="Presence" height={LANE_HEIGHT.rows(3)} summary="focused, away and asleep" definition={LANE_DEFINITIONS.presence} rows={l.presenceRows.map((r) => r.name)}>
          <RowTrack rows={l.presenceRows} interact={on.presence} />
        </Lane>
      )}
      <Lane label="Friction" height={LANE_HEIGHT.rows(3)} summary={`${l.friction.length} marks`} rows={frictionRowLabels(l.friction)}>
        <FrictionTrack marks={l.friction} interact={on.friction} />
      </Lane>
      </>
    );
  }, [d, l, per, cb, presence]);
  return (
    <Timeline {...l.window} label="Activity" header={header} selection={range} onSelect={onRange} brushMode="range" nowTip={d.now ? nowTip(d.now, true) : undefined}>
      {lanes}
    </Timeline>
  );
}

const windowLabelOf = (d: PersonTimeline) => windowLabel(d.from, d.to);
/** "hourly", "daily", "15-min bins". */
const binWord = (s: number) => (s === 3600 ? "hourly" : s === 86_400 ? "daily" : `${Math.round(s / 60)}-min bins`);

function SessionsTable(p: PersonScreenProps & { at: Date }) {
  const r = p.sessions;
  const rows = r.data?.data ?? [];
  const meta = r.data?.meta;
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const caps = p.person.data?.capabilities;
  // All agents for a person with more than one agent: only the common columns, with Agent (§11M 2.7).
  // Scoped to Claude Code it's the canonical table; scoped to Codex, the common columns without Agent.
  const scope = p.agentScope ?? "all";
  const agents = new Set([...personAgents(p), ...rows.map((s) => s.agent)]);
  const only = scope === "all" ? (agents.size === 1 ? [...agents][0] : null) : scope;
  const view: SessionTableView = only === null ? "mixed" : only === "claude-code" ? "claude-code" : "codex";
  // Subagent and review threads nest under their parent, collapsed by default (§11J gap 3).
  const ids = new Set(rows.map((s) => s.sessionId));
  const nested = rows.some((s) => isNested(s, ids));
  const toggle = (id: string) => setOpen((o) => { const n = new Set(o); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  return (
    <RegionState
      status={r.status} slow={slowNotice(r, "sessions")}
      hasData={!!r.data}
      skeleton={<SessionTable view={view} loading />}
      error={err(r, () => p.onRetry("sessions"))}
      empty={<EmptyState title="No sessions in this range" compact />}
      label="sessions"
    >
      <SessionTable view={view} expandable={nested} footer={meta && <Pager meta={meta} page={p.page} onPage={p.onPage} loading={r.status === "refetching"} />}>
        {rows.map((s) => {
          const child = isNested(s, ids);
          if (child && !open.has(s.origin?.sessionId ?? "")) return null;
          return (
            <SessionRow
              key={s.sessionId}
              session={s}
              href={p.sessionHref(s.sessionId)}
              now={p.now}
              agentColumn={view === "mixed" ? { client: s.agent === "codex" ? s.client : null } : undefined}
              common={view === "codex"}
              nesting={
                child
                  ? { kind: "child", agentName: s.origin?.agentName ?? (s.origin?.kind === "review" ? "review" : "subagent") }
                  : s.children
                    ? { kind: "parent", expanded: open.has(s.sessionId), onToggle: () => toggle(s.sessionId) }
                    : undefined
              }
              threadFacts={child ? null : threadFacts(s)}
              absent={{ missed: !!caps && !reports(caps, s.agent, "missed") }}
            />
          );
        })}
      </SessionTable>
    </RegionState>
  );
}

function RequestsTable(p: PersonScreenProps & { at: Date }) {
  const r = p.requests;
  const rows = r.data?.data ?? [];
  const meta = r.data?.meta;
  const agents = personAgents(p);
  const scope = p.agentScope ?? "all";
  // All agents for a linked person (§11M 3.3): the shared chips and an agent-dot column; one agent: its own chips.
  const shared = scope === "all" && agents.length > 1;
  const only: Agent = scope === "all" ? (agents[0] ?? "claude-code") : scope;
  const channels: RequestChannel[] = shared
    ? sharedRequestChannels(r.data?.kindCounts)
    : (AGENT_CHANNELS[only] ?? AGENT_CHANNELS["claude-code"] ?? []).map((c) => ({ ...c, count: r.data?.kindCounts[c.channel] ?? 0 }));
  const hiddenCount = r.data?.hiddenCount ?? 0;
  return (
    <>
      <div className="person-filters">
        <TextInput id="person-requests-filter" label="Filter" placeholder="path, status, session…" value={p.q} onChange={p.onQ} pending={r.status === "refetching"} />
        {p.notCaptured && <FilterChip label="▲ not captured" removeLabel="Remove filter: not captured" onRemove={() => p.onNotCaptured(false)} />}
        <RequestFilters
          channels={channels}
          value={p.kinds as Channel[]}
          onChange={p.onKinds}
          hidden={p.onShowHidden && (hiddenCount > 0 || p.showHidden) ? { count: hiddenCount, shown: !!p.showHidden, onChange: p.onShowHidden } : null}
        />
      </div>
      <RegionState
        status={r.status} slow={slowNotice(r, "requests")}
        hasData={!!r.data}
        skeleton={<RequestTable scope="person" agentDot={shared} loading />}
        error={err(r, () => p.onRetry("requests"))}
        empty={p.notCaptured ? <NoUncaptured {...p} /> : <EmptyState title={p.q || p.kinds.length ? "No requests match" : "No requests in this range"} compact />}
        label="requests"
      >
        <RequestTable scope="person" agentDot={shared} footer={meta && <Pager meta={meta} page={p.page} onPage={p.onPage} loading={r.status === "refetching"} />}>
          {rows.map((row) => (
            <RequestRow
              key={row.requestId}
              row={row}
              showSession
              agentDot={shared}
              channel={shared ? sharedChannel(row.kind) : undefined}
              detail={requestDetail(row, { withPrompt: true })}
              sessionHref={p.sessionHref}
              now={p.now}
              onOpen={p.onOpenRequest}
              selected={p.drawer?.requestId === row.requestId}
            />
          ))}
        </RequestTable>
      </RegionState>
    </>
  );
}

/** The not-captured filter matched nothing (handoff §11E.2): the active filters, what coverage says, and ways out. */
function NoUncaptured(p: PersonScreenProps) {
  const at = new Date(p.now);
  const reported = p.summary.data?.coverage.reported ?? null;
  const span = p.range ? ` between ${formatRange(p.range.from, p.range.to, at)}` : "";
  const body =
    reported === null
      ? `The proxy captured every call Claude Code reported${span || " in this window"}.`
      : `Claude Code reported ${formatCount(reported)} model ${reported === 1 ? "call" : "calls"}${span || " in this window"}, and the proxy captured ${reported === 1 ? "it" : `all ${formatCount(reported)}`}.`;
  return (
    <EmptyState
      chip={
        <div className="flex flex-wrap gap-2">
          {p.range && <Chip tone="range">{`range ${formatRange(p.range.from, p.range.to, at)}`}</Chip>}
          <FilterChip label="▲ not captured" />
        </div>
      }
      title={p.range ? "No uncaptured calls in this range" : "No uncaptured calls"}
      body={body}
      actions={
        <>
          <Button onClick={() => p.onNotCaptured(false)}>Clear filter</Button>
          {p.range && <Button onClick={() => p.onRange(null)}>Clear range</Button>}
        </>
      }
    />
  );
}

function Pager({ meta, page, onPage, loading }: { meta: Schemas["Meta"]; page: number; onPage: (p: number) => void; loading?: boolean }) {
  const size = meta.pageSize ?? 20;
  const total = meta.total ?? 0;
  if (total <= size) return null;
  const from = (page - 1) * size + 1;
  return <Pagination page={page} pageCount={Math.ceil(total / size)} from={from} to={Math.min(total, page * size)} total={total} loading={loading} onPage={onPage} inset />;
}
