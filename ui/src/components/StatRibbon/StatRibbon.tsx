import { StatTile, type StatTileProps } from "../StatTile/StatTile";

export type StatRibbonProps = { tiles: StatTileProps[]; loading?: boolean; label?: string };

/** Tiles edge to edge, split by hairlines, in one frame (handoff §6.3; People "StatRibbon"). */
export function StatRibbon({ tiles, loading, label = "Summary" }: StatRibbonProps) {
  return (
    <div role="group" aria-label={label} className="stat-ribbon flex flex-wrap border border-line bg-surface">
      {tiles.map((t) => (
        <StatTile key={t.label} {...t} loading={loading || t.loading} />
      ))}
    </div>
  );
}
