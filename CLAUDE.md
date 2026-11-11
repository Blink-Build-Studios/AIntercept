# aintercept: notes for coding agents

- **Setting aintercept up for someone** (a fresh clone to the People page with live Claude Code traffic): follow `docs/SETUP.md`. `make up` starts everything, `make verify` proves it, and routing Claude Code (step 4) needs the person, because it uses sudo.
- **Before every commit, run `make check`.** It runs exactly the checks CI requires. A commit that fails it won't go green.
- **UI and query API work** (`ui/`, `api/`): follow `docs/ui/BUILD_PLAN.md`. It overrides the design handoff in `docs/ui/handoff/`, and §11 lists every automated rule.
- **The definition of done is `docs/ui/requirements.json`.** Building a component means a story for each state listed there; implementing an endpoint means a sample for each scenario.
- **Never hand-write API types or samples.** Change the Pydantic models or tests in `api/`, then run `make regen`.
- **The UI needs Node 24:** run `nvm use` in `ui/`.
- **The Interceptor** (the control-plane hook, `docs/INTERCEPTOR.md`): the contract is `aintercept/interceptor/v1/interceptor.proto`. After changing it run `make proto`; `make test-interceptor` runs observe and enforce end to end through a real Envoy (needs Docker).
- **Harness parity:** `docs/harness-parity.md` tracks what each coding harness (Claude Code, Codex, …) sends, how it maps onto the common schema (common / mapped / bespoke), and what AIntercept shows. Update it whenever a capture shows something new or support lands.
- **Never commit captured traffic or real identifiers.** `data/` is gitignored, and `scripts/check-real-data.py` enforces the rest.
