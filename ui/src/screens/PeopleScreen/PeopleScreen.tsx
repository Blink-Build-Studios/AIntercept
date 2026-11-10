import { AboutThisData, type AboutState } from "../../components/AboutThisData/AboutThisData";
import type { Schemas } from "../../api/types";
import { slowNotice, type Region } from "../../lib/region";
import { formatCount, formatPct } from "../../lib/format";
import { formatDay } from "../../lib/time";
import { AGENT_NAME, type Agent } from "../../components/AgentChip/AgentChip";
import { AgentCard, agentSummary } from "../../components/AgentCard/AgentCard";
import { AppBar } from "../../components/AppBar/AppBar";
import { Banner, type BannerProps } from "../../components/Banner/Banner";
import { Chip } from "../../components/Chip/Chip";
import { PeopleTable } from "../../components/PeopleTable/PeopleTable";
import { EmptyState } from "../../components/EmptyState/EmptyState";
import { PageHeader } from "../../components/PageHeader/PageHeader";
import { UpdatedAgo, type UpdatedAgoProps } from "../../components/UpdatedAgo/UpdatedAgo";
import { Pagination } from "../../components/Pagination/Pagination";
import { PersonRow, PersonRowSkeleton } from "../../components/PersonRow/PersonRow";
import { RegionState } from "../../components/RegionState/RegionState";
import { StatRibbon } from "../../components/StatRibbon/StatRibbon";
import { TextInput } from "../../components/TextInput/TextInput";
import { UnattributedNotice } from "../../components/UnattributedNotice/UnattributedNotice";
import { WaitingChecklist, waitingSteps } from "../../components/WaitingChecklist/WaitingChecklist";
import "./PeopleScreen.css";

export type { Agent };

export const WINDOWS = [
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "14d", label: "14d" },
  { value: "all", label: "All" },
];

export type PeopleScreenProps = {
  /** "About this data": the AppBar button and the drawer (`?about=1`). */
  about?: AboutState;
  /** The time window (24h | 7d | 14d | all), and the one whose request is in flight. */
  window: { value: string; onChange: (w: string) => void; pending?: string | null };
  /** The range the window covers, for the subtitle ("Oct 22 – Nov 4"). */
  range: { from: string; to: string };
  /** The page's freshness, in place of LIVE (§11G.C1). */
  updated: UpdatedAgoProps;
  /** `/status`: before the first request the page is the setup checklist. */
  status?: Schemas["Status"];
  overview: Region<Schemas["Overview"]>;
  people: Region<Schemas["PeopleResponse"]>;
  onRetry: (region: "overview" | "people") => void;
  /** The AgentCard filter (one agent, or all). */
  agent: Agent | null;
  onAgent: (a: Agent | null) => void;
  q: string;
  onQ: (q: string) => void;
  onPage: (page: number) => void;
  personHref: (personId: string) => string;
  /** Delayed data or a failed refresh (§5.3). */
  banner?: BannerProps;
  /** "now", for last-seen times and the checklist. */
  now: string;
};

const RIBBON_LABELS = ["People", "Agents seen", "Sessions", "Requests", "Devices"];

/** The window in words, for the subtitle (§11G.C1). */
const WINDOW_WORDS: Record<string, string> = { "24h": "24 hours", "7d": "7 days", "14d": "14 days" };

/** The ribbon from `/overview` (handoff §7.1). */
function ribbon(o: Schemas["Overview"]) {
  const seen = o.agents.filter((a) => a.people > 0);
  return [
    { label: "People", value: o.people, sub: `${formatCount(o.activeNow)} active now` },
    { label: "Agents seen", value: seen.length, sub: seen.map((a) => AGENT_NAME[a.agent]).join(", ") || "none yet" },
    { label: "Sessions", value: o.sessions, sub: o.unattributed.sessions ? `+${formatCount(o.unattributed.sessions)} not tied to a person` : "all tied to a person" },
    { label: "Requests", value: o.requests, sub: "all agents, all channels" },
    // §11L.1: Missed is Claude Code's, so it moves to that agent's card; the ribbon keeps what every agent has.
    { label: "Devices", value: o.devices, sub: "across all agents" },
  ];
}

/**
 * An agent card's second and third lines (handoff §11L.1): the agent's headline where it has
 * one (Claude Code's missed calls and bypass, Codex's highest weekly window), else its shape.
 */
export function agentLines(a: Schemas["AgentOverview"]): { summary: string; sub: string } {
  const summary = agentSummary(a);
  const shape = a.shape.replace(/→/g, " → ");
  if (a.agent === "claude-code") {
    const sessions = summary.split(" · ")[0] ?? summary;
    return {
      summary: a.missedCalls ? `${sessions} · ▲ ${formatCount(a.missedCalls)} calls missed` : summary,
      sub: a.bypassPct !== null ? `bypass on ${formatPct(a.bypassPct)} of model calls` : shape,
    };
  }
  if (a.agent === "codex" && a.highestWeekPct !== null) return { summary, sub: `limits: highest week ${Math.round(a.highestWeekPct)}%` };
  return { summary, sub: shape };
}

/** A Codex view's cells (§11L.1): the 5-hour and weekly windows used, and the plan. */
function codexCells(p: Schemas["PersonSummaryRow"]) {
  const l = p.agentLimits.find((x) => x.agent === "codex");
  if (!l) return { limits: null, plan: null };
  const used = (name: string) => l.windows.find((w) => w.name === name)?.usedPct ?? null;
  const limits = l.windows.length ? [used("5h"), used("week")] : null;
  return { limits, plan: l.plan };
}

/**
 * The People page (handoff §7.1): everyone whose AI traffic passed through the
 * proxy in the window. Before the first request it is the setup checklist.
 */
export function PeopleScreen(p: PeopleScreenProps) {
  const o = p.overview.data;
  const rows = p.people.data;
  const appBar = (
    <>
      <AppBar
        breadcrumb={[{ label: "people" }]}
        window={{ ...p.window, options: WINDOWS, "aria-label": "Active in the last" }}
        windowLabel="Active in the last"
        updated={<UpdatedAgo {...p.updated} />}
        onAbout={p.about?.onOpen}
      />
      {p.about?.open && <AboutThisData contentRetained={p.about.contentRetained} products={p.about.products} onClose={p.about.onClose} />}
    </>
  );
  if (p.status && p.status.firstRequestAt === null) {
    return (
      <>
        {appBar}
        <PageHeader title="People" subtitle="waiting for the first request through the proxy" />
        <main className="people-screen page-enter">
          <WaitingChecklist steps={waitingSteps(p.status, new Date(p.now))} checkedAt={p.now} />
        </main>
      </>
    );
  }
  const filterLabel = p.agent ? `${AGENT_NAME[p.agent]} · ${rows ? formatCount(rows.meta.total ?? 0) : "…"}` : `all agents · ${rows ? formatCount(rows.meta.total ?? 0) : "…"}`;
  const view = p.agent ?? "all";
  const busiest = Math.max(1, ...(rows?.data ?? []).flatMap((r) => r.activity.map((d) => d.count)));
  const page = rows?.meta.page ?? 1;
  const size = rows?.meta.pageSize ?? 50;
  const total = rows?.meta.total ?? 0;
  return (
    <>
      {appBar}
      {p.banner && <Banner {...p.banner} page />}
      <PageHeader
        title="People"
        subtitle={
          WINDOW_WORDS[p.window.value]
            ? `active in the last ${WINDOW_WORDS[p.window.value]} · ${formatDay(p.range.from)} – ${formatDay(p.range.to)} · counts below cover that window only`
            : `active since ${formatDay(p.range.from)} · counts below cover that window only`
        }
      />
      <main className="people-screen page-enter">
        <RegionState
          status={p.overview.status} slow={slowNotice(p.overview, "the overview")}
          hasData={!!o}
          label="the overview"
          skeleton={<StatRibbon loading tiles={RIBBON_LABELS.map((label) => ({ label, value: null }))} />}
          error={p.overview.error && { ...p.overview.error, onRetry: () => p.onRetry("overview") }}
        >
          {o && (
            <div className="people-overview">
              <StatRibbon label="People overview" tiles={ribbon(o)} />
              <div className="people-agents">
                {o.agents.map((a) => (
                  <AgentCard
                    key={a.agent}
                    agent={a.agent}
                    people={a.people}
                    {...agentLines(a)}
                    selected={p.agent === a.agent}
                    onClick={() => p.onAgent(p.agent === a.agent ? null : a.agent)}
                  />
                ))}
              </div>
              {o.unattributed.sessions > 0 && o.unattributed.firstSeen && (
                <UnattributedNotice
                  sessions={o.unattributed.sessions}
                  modelCalls={o.unattributed.modelCalls}
                  firstSeen={o.unattributed.firstSeen}
                  reason={o.unattributed.reason}
                  href={p.personHref("unattributed")}
                />
              )}
            </div>
          )}
        </RegionState>
        <section aria-label="People" className="people-table">
          <div className="people-filters">
            <TextInput id="people-search" label="Search" value={p.q} onChange={p.onQ} placeholder="name, email, account, device…" pending={p.people.status === "refetching"} />
            <Chip tone="selected">{filterLabel}</Chip>
            <span className="flex-1" />
            <span className="type-data text-ink-faint">sorted by last seen</span>
          </div>
          <RegionState
            status={p.people.status} slow={slowNotice(p.people, "people")}
            hasData={!!rows}
            label="people"
            skeleton={
              <PeopleTable view={view}>
                {Array.from({ length: 5 }, (_, i) => (
                  <PersonRowSkeleton key={i} view={view} />
                ))}
              </PeopleTable>
            }
            empty={<EmptyState compact title="No people match" body={p.q ? `Nothing matches “${p.q}”.` : undefined} />}
            error={p.people.error && { ...p.people.error, onRetry: () => p.onRetry("people") }}
          >
            {rows && (
                <PeopleTable
                  view={view}
                  empty={<EmptyState compact bare title="No people match" body={p.agent ? `No one in this window uses ${AGENT_NAME[p.agent]}.` : "Try a shorter search."} />}
                  footer={
                    total > size ? (
                      <Pagination page={page} pageCount={Math.ceil(total / size)} from={(page - 1) * size + 1} to={Math.min(page * size, total)} total={total} onPage={p.onPage} inset />
                    ) : undefined
                  }
                >
                  {rows.data.length
                    ? rows.data.map((r) => (
                        <PersonRow
                          key={r.personId}
                          person={r}
                          href={p.personHref(r.personId)}
                          activityMax={busiest}
                          now={p.now}
                          view={view}
                          {...(view === "codex" ? codexCells(r) : {})}
                        />
                      ))
                    : undefined}
                </PeopleTable>
            )}
          </RegionState>
          <p className="type-data text-ink-faint people-footnote">Tokens: exact where the agent reports usage, ≈ estimated from text size, — where the surface sends none.</p>
        </section>
      </main>
    </>
  );
}

