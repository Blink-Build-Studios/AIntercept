import "./DrawerMeta.css";

/**
 * The muted line under the drawer header (handoff §6.6): time, prompt, step,
 * request class and id, joined with " · " ("Nov 3 10:42:16.204 · prompt #19 · step 2 of 9 · main · req_01H…9fa").
 */
export function DrawerMeta({ parts }: { parts: (string | null | undefined | false)[] }) {
  return <p className="drawer-meta type-data text-ink-muted">{parts.filter(Boolean).join(" · ")}</p>;
}
