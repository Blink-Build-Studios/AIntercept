import { formatCount } from "../../lib/format";
import { AgentChip, type Agent } from "../AgentChip/AgentChip";
import "./PersonIdentity.css";

/** One account of a person: its agent, "ChatGPT acct 9c2e…41 · Plus", and its devices. */
export type PersonAccount = { agent: Agent; account: string; devices: number };

export type PersonIdentityProps = {
  name: string | null;
  email: string | null;
  /** How the accounts were joined ("email"); only `linkedBy` ever merges accounts (§11J screens 5). */
  linkedBy: string | null;
  accounts: PersonAccount[];
};

/**
 * Who a person is, at the top of their page (handoff §11J screens 5): the name, the
 * email and how accounts were linked, then each account under it: agent chip, account
 * and plan, devices.
 */
export function PersonIdentity({ name, email, linkedBy, accounts }: PersonIdentityProps) {
  const sub = [email, linkedBy && `linked by ${linkedBy}`].filter(Boolean).join(" · ");
  return (
    <div className="person-identity bg-surface">
      <div className="person-identity-title">
        <h1 className="type-title text-ink">{name ?? "name unknown"}</h1>
        {sub && <span className="type-data text-ink-muted">{sub}</span>}
      </div>
      <div className="person-identity-accounts">
        {accounts.map((a) => (
          <div key={`${a.agent}/${a.account}`} className="contents">
            <span>
              <AgentChip agent={a.agent} />
            </span>
            <span className="text-ink-muted">{a.account}</span>
            <span className="text-ink-muted">{`${formatCount(a.devices)} ${a.devices === 1 ? "device" : "devices"}`}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
