import { delayedSince } from "../../api/hooks/useLive";
import { usePeoplePage } from "../../api/hooks/usePeoplePage";
import { PeopleScreen, type Agent } from "../../screens/PeopleScreen/PeopleScreen";
import { aboutProducts, aboutState } from "../../api/hooks/aboutState";
import { useDocumentTitle } from "../../lib/useDocumentTitle";
import { useUrlState } from "../useUrlState";

/** `/people` (handoff §7.1): window, agent filter, search and page in the URL; polled live. */
export function PeoplePage() {
  const [url, set] = useUrlState();
  useDocumentTitle();
  const win = url.win ?? "14d";
  const d = usePeoplePage({ win, agent: url.agent, q: url.q ?? "", page: url.page });
  const now = d.overviewMeta?.asOf ?? new Date().toISOString();
  const delayed = delayedSince(d.overviewMeta, (d.overview.data?.activeNow ?? 0) > 0);
  const banner = d.refresh.failed
    ? { kind: "refreshFailed" as const, since: d.refresh.since ?? now, retryInS: d.refresh.retryInS, onRetry: d.refresh.retry, retrying: d.refresh.retrying, now: new Date(now) }
    : delayed
      ? { kind: "delayed" as const, since: delayed, now: new Date(now) }
      : undefined;
  return (
    <PeopleScreen
      window={{ value: win, onChange: (w) => set({ win: w === "14d" ? undefined : w }), pending: d.overview.status === "refetching" ? win : null }}
      range={{ from: d.range.from ?? d.overview.data?.unattributed.firstSeen ?? now, to: now }}
      updated={d.updated}
      status={d.live.status?.data}
      overview={d.overview}
      people={d.people}
      onRetry={(r) => d.retry[r]()}
      agent={(url.agent[0] as Agent | undefined) ?? null}
      onAgent={(a) => set({ agent: a ? [a] : [] })}
      q={url.q ?? ""}
      onQ={(q) => set({ q }, { replace: true })}
      onPage={(page) => set({ page })}
      personHref={(id) => `/people/${encodeURIComponent(id)}`}
      banner={banner}
      now={now}
      about={aboutState(url.about, set, d.live.status?.data, aboutProducts(d.overview.data))}
    />
  );
}
