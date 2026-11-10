# Answers from the captured data

Everything below comes from the captured traffic (Oct 22 – Nov 4), not from the brief. Claude Code only, excluding load-test and probe traffic: **1,548 model calls, 969 analytics batches holding 21,353 events of 128 types, 9 feature-flag fetches with full bodies, and 384 other Claude Code requests.**

Your hierarchy and terminology fixes are right: the context gauge belongs on the session page, and flags and friction are separate things. Go ahead with the session page and the request drawer.

## Corrections to your table

- **Presence is per session, but sleep is per device.**
  - Focus: `tengu_return_to_session` reports `blurDurationMs` (how long that terminal was out of focus). It's per session, since each Claude Code process watches its own terminal.
  - Sleep: `tengu_event_loop_stall` carries `likely_sleep: true` and `clock_jump_ms`. Every open session on the machine reports the same sleep.
  - So on the person page, combine by device: sleep is the union of the sessions' sleep windows, deduplicated. "Away" means every open session was blurred at once.
- **Flags are account-level in practice, but they change during the day.** See the flags answer. Show a current set with a "changed since" count, not a static list.
- **Capabilities really do differ by session** (by project and config). Combining them is honest if it's labeled "seen in any session", with a per-session breakdown one click away.
- **Permission settings change within a session.** Treat them as a timeline, not a per-session property.
- **`key_hash` isn't an identity.** One session used 7 different `key_hash` values because the OAuth token refreshes. Identify people by `account_uuid` and devices by `device_id`, never by `key_hash`.

## Answers

**1. Field to level.** Lowest level each field attaches to, and the key that links it:

| Level | Fields / events | Join key |
|---|---|---|
| Account | plan (`subscription_type`), `rateLimitTier`, `organizationRole`, `subscriptionCreatedAt`, `firstTokenTime`, org ID, feature flags; rate-limit response headers | `account_uuid` |
| Device | OS, arch, terminal, shell, node version (`env.*` on every event); sleep (`likely_sleep`) | `device_id` |
| Session | repo and `head_sha`, linked PR (`tengu_session_linked_to_pr`), capabilities (tools, MCP servers, skills), context size and compaction, recaps, `tengu_return_to_session` (focus) | `session_id` (header on model calls, field on every event) |
| Prompt | `tengu_input_prompt` (length, typed vs pasted, `is_negative`), `tengu_turn_end` (duration, result), `tengu_cancel`, `tengu_feature_sad` / `_bad` | `cc_prompt_id` = `x-claude-code-prompt-id` |
| Request (step) | model, tokens, cost, stop reason, `tengu_api_query`, `tengu_api_success` (with `permissionMode`) | `requestId` = response `request-id`; `clientRequestId` = `x-client-request-id` |
| Tool call | `tengu_tool_use_*`, `tengu_bash_tool_command_executed` / `_failed`, file operations | `toolUseID`, plus `requestId` / `messageID` of the step that issued it |

Subagent calls carry `x-claude-code-agent-id` and `agent-type` on the request and `agent_id` on their events. They share the parent's `cc_prompt_id`.

**2. Session IDs.**
- **Model calls:** all 1,548 carry `x-claude-code-session-id`. `count_tokens` (12) carries it too.
- **Analytics batches:** the request has no session header, but every event inside has `session_id` (21,353 of 21,353). No batch ever mixed sessions (0 of 969).
- **Flags (`/api/eval/sdk-*`):** no header, but the request body has `attributes.sessionId`, so they're attributable.
- **No session at all:** 384 requests, all account or setup calls:
  - skills list and downloads (185);
  - MCP registry and directory (~170);
  - `bootstrap`, profile, settings, usage, plugins, quota (the rest).

  Of these, 338 carry a `key_hash`, so each can be tied to a session by token and time. 46 can't.

  Show them in the person's "All requests" log, not under any session.

**3. Flags.**
- Fetched at session start, then about every 6 hours per running session.
- The request carries account, device, session, plan, `rateLimitTier`, `organizationRole`, `subscriptionCreatedAt`, `firstTokenTime`, `appVersion`.
- **They change during a day:** the flag count grew 1,072 → 1,108 over two days, and each 6-hour fetch changed 0–12 values.
- One experiment-assignment event (`GrowthbookExperimentEvent`: experiment ID and variation) showed up in analytics.
- **Design:** an account-level "current flags" summary, plus a small "N changed since <time>" marker. No timeline lane.

**4. Presence.** Covered above.
- 18 `tengu_return_to_session` events, with blur durations from 6 minutes to 14 hours.
- `tengu_idle_return_action` covers returning after idle (e.g. 82 minutes idle with a 348k-token context).
- Sleep stalls go up to about 18 minutes per sample.
- **Two sessions open at once:** focus counts as active if either session is focused. Sleep comes from the device.

**5. Capabilities.** Yes, they differ by project. Same person, same day (Oct 23):
- one session had 20 tools, with MCP `playwright`;
- another had 51 tools across 6 MCP servers (Docs, Gmail, Drive, Vercel, taza, playwright).

Caveat: newer sessions load most tools lazily (tool search). The first request lists about 16 tools, and more arrive later (`tengu_deferred_tools_pool_change`). So "capabilities" has to mean the whole session, not just its first request.

**6. Permission settings.** These change within a session. `permissionMode` is on every `tengu_api_query` and `tengu_api_success`. In the main session it went `bypassPermissions`, then `default` and back again (twice) within 20 seconds on Nov 3 (someone toggling it). Show it as a state band on the session timeline. The person page summary should be "bypassed approvals for X% of model calls", not "X of Y sessions".

**7. Prompt IDs.**
- **Model calls:** 1,517 of 1,548 (98%) carry one:
  - main 1,360 of 1,362;
  - subagent 126 of 126 (same ID as the prompt that spawned them);
  - auxiliary 31 of 56;
  - compaction 0 of 4.
- **Analytics events:** 21,037 of 21,353 (98.5%).
- **No prompt:**
  - compaction always, and about half of auxiliary calls (recaps, suggestions, idle compaction). Attach these to the session, between prompts.
  - Idle compaction is its own event (`tengu_idle_compact_fired`, e.g. at a 904k-token context).

**8. Across time and machines.**
- **Across days and networks: yes.** The main session ran Nov 2 → Nov 4 from three different IP addresses (home, another network, IPv6 test).
- **Across devices:** never seen. A session is one process on one device.
- **`device_id`:** on every analytics event. On model calls it comes from parsed `metadata.user_id`, which older rows (before Nov 2) don't have. The design can assume it's always present.

**9. The 22:40–00:57 tail.** These are the main session's own analytics. The model calls didn't stop: they bypassed the proxy. That network had IPv6, and the model calls went straight to Anthropic over it while the analytics still came through. It's now fixed.
- **Nov 3, 00:00 hour:** 37 `tengu_api_success` events, none matched to a captured call.
- **Nov 3, 15:00–17:59:** another 57, same cause.

This is exactly what the coverage check (§D) is for. Design it to show these windows as "reported, not captured".

**10. Real sample.**
- **Person 1 (main sample):** one account, one org, one device, 11 sessions with model calls. Older sessions (Oct 23–29) lack identity fields because those rows predate the parser.
  - **The main session:** 640 calls, 21,305 events, 3 days.
  - **Two Oct 23 sessions:** 539 and 282 calls, with different capabilities.
- **A second account:** a different org and device. 1 session (4 model calls Oct 29, events Nov 2–3, 48 events).
- **One more session** (Nov 2, 38 calls) from a different IP, with no identity parsed yet.

For a second real person with a full session, we need to route someone else's Claude Code through the proxy. Until then, use person 1 and mark invented sessions as illustrative.
