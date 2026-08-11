# AGENTS.md

This repo plans a greenfield effort — there is no production code yet. Most work here is **planning**: resolving wayfinder decision tickets (GitHub Issues, mirrored in `.scratch/`).

## Agent skills

### Issue tracker

Issues live on GitHub (`yuefanxiao/DataIntelligent`), mirrored to local markdown under `.scratch/`. GitHub is canonical; any write happens on GitHub first. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary — `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout — `CONTEXT.md` + `docs/adr/` at the repo root, created lazily by `/domain-modeling`. See `docs/agents/domain.md`.

## Working conventions

- This effort's map: GitHub issue「Map: Enterprise Data Intelligence Layer」(label `wayfinder:map`).
- Resolving a wayfinder ticket: claim it first (assign yourself), consult `/grilling` and `/domain-modeling`, then post the answer, close, and update the map's Decisions-so-far.
- Decision context: the originating discussion is the design summary in `docs/decision-discussion.md`.
