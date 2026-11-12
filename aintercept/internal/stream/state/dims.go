package state

import "github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"

// The dimension tables (docs/STREAMING_PIPELINE.md §4.6): small facts each key
// states about sessions, accounts, credentials and devices. Nothing is stored
// per person: the query resolves people from these (email groups over
// dim_account, then accounts → sessions → credentials), so a new link changes
// one row, not history.
const (
	TableAccounts          = "dim_account"            // per account key: who it is, each field the latest sighting's
	TableSessionAccounts   = "dim_session_account"    // an account a session's visible call or Claude Code event names
	TableSessionVisible    = "dim_session_visible"    // a session with a visible model call (unattributed when no account)
	TableSessionPerson     = "dim_session_person"     // a session's person, as the people list counts it (least account named)
	TablePersonDevices     = "dim_person_device"      // a person and device a model call or event names together
	TableSessionCredential = "dim_session_credential" // a credential a session's visible calls or analytics batches used
	TableAccountEvents     = "dim_account_events"     // per key: what an account's Claude Code events here say (org, plan)
	TableAccountDevices    = "dim_account_device"     // per key: an account's device, from its events (with env) or calls
	TableCodexPlans        = "dim_codex_plan"         // per key: an account's newest Codex plan here
)

// Pick is a value and when it was seen; the query keeps the newest (the
// greatest at the same time).
type Pick struct {
	V  string
	TS int64
	OK bool
}

type SessionAccount struct{ SessionID, AccountID string }
type SessionVisible struct{ SessionID string }
type SessionPerson struct{ SessionID, PersonID string }
type PersonDevice struct{ PersonID, DeviceID string }
type SessionCredential struct{ SessionID, KeyHash string }

// AccountEvents is what one key's Claude Code events say about an account: its
// earliest org (the least at the same time) and newest plan.
type AccountEvents struct {
	AccountID string
	Org       string
	OrgNS     int64
	Plan      Pick
}

// AccountDevice is an account's device as one key's records name it: OnEvents
// when an event does, with each env field's newest value.
type AccountDevice struct {
	AccountID, DeviceID string
	OnEvents            bool
	Env                 [3]Pick // platform, terminal, shell
}

// CodexPlan is an account's newest Codex plan in one key.
type CodexPlan struct {
	AccountID string
	Plan      Pick
}

func (s *KeyState) dims(r records.Record, d *Delta) {}
