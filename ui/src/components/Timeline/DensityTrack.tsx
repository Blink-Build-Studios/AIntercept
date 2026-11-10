import type { Channel } from "../ChannelSwatch/ChannelSwatch";
import { Bars, FILL, type BinInteraction } from "./BarTrack";
import { Track } from "./Lane";

/** Like BarTrack, at 85% opacity and with no gaps (analytics, §6.4); bins are interactive as BarTrack's. */
export function DensityTrack({ bins, binSeconds, color, maxV, interact }: { bins: { t: string; v: number }[]; binSeconds: number; color: Channel; maxV?: number; interact?: BinInteraction }) {
  return (
    <Track interactive={!!interact}>
      <Bars bins={bins} binSeconds={binSeconds} maxV={maxV} interact={interact} className={() => `opacity-85 ${FILL[color]}`} />
    </Track>
  );
}
