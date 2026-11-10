// Times in the viewer's zone (handoff §3.3): HH:mm today, "MMM D HH:mm" otherwise;
// with `seconds`, HH:mm:ss (request rows).

const pad = (n: number) => String(n).padStart(2, "0");
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function formatTime(iso: string, now: Date = new Date(), seconds = false): string {
  const d = new Date(iso);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}${seconds ? `:${pad(d.getSeconds())}` : ""}`;
  const sameDay = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? hm : `${MONTHS[d.getMonth()]} ${d.getDate()} ${hm}`;
}

/** Always with the day, "Nov 3 10:25" (prompt rows, handoff §11B.4: a session spans days). */
export function formatDayTime(iso: string): string {
  const d = new Date(iso);
  return `${MONTHS[d.getMonth()]} ${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A range, naming the day once when both ends share it: "Nov 2 16:48–16:57", "09:00–09:25". */
export function formatRange(from: string, to: string, now: Date = new Date()): string {
  const a = new Date(from);
  const b = new Date(to);
  const sameDay = a.toDateString() === b.toDateString();
  const end = formatTime(to, now);
  return `${formatTime(from, now)}–${sameDay ? end.slice(-5) : end}`;
}

/** The day alone, "Oct 22" (first-seen dates). */
export function formatDay(iso: string): string {
  const d = new Date(iso);
  return `${MONTHS[d.getMonth()]} ${d.getDate()}`;
}

/** A request's moment, to the millisecond, with the day: "Nov 3 10:42:16.204" (the drawer's meta line). */
export function formatStamp(iso: string): string {
  const d = new Date(iso);
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  return `${formatDayTime(iso)}:${pad(d.getSeconds())}.${ms}`;
}
