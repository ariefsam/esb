# `esb ui` — Ideas & Implementation Plan for Web-Based UI

**Branch:** `kanban/http-web-based-ui-08543e551fa5bfc7`
**Repo:** `github.com/ariefsam/esb` (this worktree)
**Goal:** Add a local, command-driven web UI to the `esb` scaffolding tool so users can explore the generated event-sourcing project from a browser instead of grep-ing the codebase. The HTTP scaffold (cd72a24 / 82409ae / 350eec9) is already on this branch — this plan covers what it should *become*, not redo it.

---

## 1. Why bother?

The CLI (`esb init`, `esb add aggregate`, `esb add event`, `esb add handler`, `esb add query`) is fast but text-blind. After three rounds of `esb add` the user holds a tree of `domain/*.go`, `projection/*_row.go`, `projection/*_worker.go`, `server/handler/*.go`, and the only way to know "what aggregates do I have, what events do they emit, which handlers exist" is `grep` or `find`. The web UI is a **read-mostly, single-page cockpit** that answers those questions in one tab — and gives the user a way to fire the same CLI commands from the browser without typing them again.

Two strategic reasons to invest:

1. **Adoption surface for non-Go contributors.** The target user of an event-sourcing boilerplate is a backend team, but their PMs / reviewers want to *see* domain shape, not read Go. A web UI lowers the bus-factor for new joiners.
2. **Demonstrates that the generator is correct.** If `esb ui` can parse what was generated and reflect it accurately, that parsing logic doubles as an end-to-end smoke test for every other `esb` command.

Constraints agreed upfront:

- Local-only. Bind to `127.0.0.1`, default `8787`. **Never** expose `0.0.0.0` without an explicit `--addr` and an opt-in confirmation.
- Read by default. Writes only happen through an allow-list of subcommands (mirrors `esb add ...` semantics; does not run arbitrary shell).
- Reuses `gopkg.in/yaml.v3` and the project's existing `embed.FS` for templates/static — no new top-level deps.
- No DB. The UI parses files at request time (cheap for project-sized trees). State that survives a refresh is in the run-store, scoped to the process lifetime.

---

## 2. Ideas (ranked, with effort + payoff)

Each idea is shaped to be implementable in one PR. Effort is in "ideal-engineer hours", excluding code review.

### A. Overview dashboard (Phase 1, already on branch)

- **What:** `/` shows module path, aggregate count, event count, handler count, projection worker count, last-3 runs.
- **Why first:** Establishes the layout, the parsing pipeline (files → domain summary), and proves the same-origin / template-error discipline that the existing commits already paid for.
- **Already shipped on branch:** `ui/server.go` parses templates; `cmd/ui.go` wires `127.0.0.1:8787` + graceful shutdown; `cmd/ui_test.go` covers the three failure modes (not-registered, non-ESB root, listen-collision).

### B. Aggregate detail page (Phase 1, already on branch)

- **What:** `/aggregates/<name>` renders the aggregate's events (table: name, fields, count of handlers referencing it), source link to each generated file, and a "show Apply() cases" section pulled from the generated `domain/*.go`.
- **Why:** Domain shape is the highest-value page in the UI.

### C. Commands palette + run history (Phase 1, already on branch)

- **What:** `/commands` renders an allow-listed set of buttons (`esb add aggregate`, `esb add event`, `esb add handler`, `esb add query`, `esb show <aggregate>`). `/commands/execute` POSTs the form, kicks off a `ProcessRunner` with capture, stores the run in memory, and `/commands/runs/<id>` streams stdout/stderr with auto-refresh until done.
- **Why:** This is the *write* path — without it the UI is read-only and won't save users from re-typing CLIs.

### D. Polish from the two fix commits (already on branch)

The two follow-up commits `82409ae` (silent template errors, dead code, same-origin) and `350eec9` (form hardening, run handling) are real artifacts of the previous plan-less implementation:

| Fix commit | Bug class | What the right plan would have prevented |
|---|---|---|
| `82409ae` | silent html/template parse errors + dead helpers + missing same-origin check | A test that asserts `tmpl, err := parseTemplates()` returns no error AND a SecurityHeaders middleware test |
| `350eec9` | command form accepts whitespace-only / path-traversal values, run store has no cap | A validator test on the form struct + a `RunStore` test for `len > N` |

**Lesson for this plan:** every new endpoint below ships with a failing test FIRST. No "ship then fix" mode.

### E. Idea — Domain graph view (Phase 2)

- **What:** Render the aggregate ↔ event ↔ handler ↔ projection row graph as an ASCII tree on `/aggregates/<name>` and a full DOT export on `/graph` (user can pipe into `dot -Tsvg`).
- **Why:** DOT export is the cheap version. Full SVG rendering means pulling a CDN script, which violates "local-only".
- **Effort:** ~4h. **Payoff:** high — DOT is the lingua franca of event-sourcing diagrams and the user can render it in their editor.

### F. Idea — Hot-reload of `domain/*` parsing (Phase 2)

- **What:** Watch the project root via `fsnotify`; when a generated file changes, invalidate in-memory parser cache and re-render.
- **Why:** User runs `esb add event order OrderShipped` in another terminal → UI updates without manual refresh.
- **Effort:** ~3h including tests. **Payoff:** high for daily workflow.
- **Risk:** `fsnotify` on macOS sometimes double-fires; debounce or accept a flag for polling fallback.

### G. Idea — Diff preview before command run (Phase 2)

- **What:** For `esb add event ...`, the UI parses what the command *would* change (using the same generator code path but in dry-run mode), shows a unified diff in the browser, and only POSTs to `/commands/execute` after the user confirms.
- **Why:** This is the #1 trust-builder for a write-capable web UI. The whole point is to reduce the chance of "I ran the wrong command and now my project is broken".
- **Effort:** ~6h because the generator has to be importable from `ui/`. **Payoff:** very high.

### H. Idea — JSON export for CI ingest (Phase 3)

- **What:** `/api/project.json` returns the full parsed domain summary (aggregates, events, handlers, projection rows, wire providers). `esb show --json` is already in the codebase — same DTO.
- **Why:** Lets CI lint "your PR added a handler but didn't add a corresponding projection row" without spinning up the UI.
- **Effort:** ~2h once the parser is shared. **Payoff:** medium.

### I. Idea — Auth gate before bind (Phase 3, only if `--addr` ≠ loopback)

- **What:** If user passes `--addr 0.0.0.0:8787` or anything not in `127.0.0.0/8` / `::1`, refuse to start unless `--allow-public` is also passed AND a shared bearer token is provided.
- **Why:** Phase 1 silently allows `--addr 0.0.0.0:8787` which is a foot-gun. The fix should be a hard wall, not a warning.
- **Effort:** ~3h including tests. **Payoff:** safety-critical.

### J. Idea — TUI fallback for headless machines (Phase 3)

- **What:** `--tui` flag renders the same overview to the terminal via `bubbletea`.
- **Why:** Useful in CI / over SSH where a browser isn't an option but the user still wants the summary.
- **Effort:** ~6h new dep. **Payoff:** medium.
- **Dep:** `github.com/charmbracelet/bubbletea`.

---

## 3. Implementation plan (phased)

The plan below assumes the worktree-per-feature workflow: each phase lands in a separate branch off `main`, with explicit commit consent from the user before `git commit`. Tests are written first per the project's TDD discipline.

### Phase 1 — Stabilize what shipped (`350eec9` is HEAD)

The HTTP scaffold works but lacks regression tests for the two bug classes that triggered `82409ae` and `350eec9`. Lock them in before adding features.

1. **T1.1** Add `ui/server_test.go::TestParseTemplates_RendersAllRoutes` — for each route registered in `Handler()`, `RenderBody` must succeed with a synthetic Server. RED: `tmpl.Execute` returns error because a route's template is missing.
2. **T1.2** Add `ui/server_test.go::TestSecurityHeaders_NoSniff_NoCacheBypass` — assert every response carries `X-Content-Type-Options: nosniff` and a `Cache-Control: no-store` header. RED: existing middleware is bare.
3. **T1.3** Add `cmd/ui_test.go::TestUI_RejectsCommandForm_EmptyName` — POST `/commands/execute` with an empty `name` field → 400, run-store unchanged. RED: existing form passes through.
4. **T1.4** Add `ui/runstore_test.go::TestRunStore_CapRejectsAt1000` — 1001st insert returns ErrStoreFull. RED: no cap.
5. **T1.5** Run `go test ./...` (with sibling-repo symlink per skill note), then **STOP** and request commit consent. Commit message: `test(ui): lock in security headers, form validation, run-store cap`.

### Phase 2 — Domain graph view (Idea E)

6. **T2.1** Add `ui/parse/project.go::Project` struct + `Parse(root string) (*Project, error)`. Returns aggregates, events, handlers, projection rows parsed from `domain/`, `server/handler/`, `projection/` (regex + light AST, no `go/ast`).
7. **T2.2** Write `ui/parse/project_test.go` with a fixture project (commit a tiny fixture under `ui/parse/testdata/`) and assert counts per file.
8. **T2.3** Add `ui/server.go::handleGraph` returning ASCII tree for `?format=text` and DOT for `?format=dot`.
9. **T2.4** Link from `/aggregates/<name>` to `/aggregates/<name>?format=dot`.
10. **T2.5** Commit "feat(ui): DOT export of domain graph".

### Phase 3 — Hot-reload (Idea F)

11. **T3.1** Add `ui/watch/watcher.go` wrapping `fsnotify` with a debouncer of 200ms.
12. **T3.2** `Server.Handler()` subscribes — on each event, a context-cancelled channel signals `s.runs` to drop, parser cache invalidates.
13. **T3.3** Test: write a file to `t.TempDir()` while watcher is alive; assert cache invalidation event fires within 500ms.
14. **T3.4** Commit "feat(ui): hot-reload project parse on file change".

### Phase 4 — Diff preview before run (Idea G)

15. **T4.1** Refactor generators so they expose `Plan(root string, args ...string) (Diff, error)` returning parsed AST-level diffs.
16. **T4.2** New route `POST /commands/plan` returns the diff as JSON.
17. **T4.3** Front-end (`/commands`): when a command has a plan, render a `<pre>` with the diff and disable the execute button until confirmed.
18. **T4.4** Test: send a known event-add plan, assert JSON output matches expected diff string byte-for-byte.
19. **T4.5** Commit "feat(ui): dry-run diff preview for command runs".

### Phase 5 — Public-bind safety (Idea I)

20. **T5.1** Add `cmd/ui.go` validation: `uiAddr` parsed, if not loopback → require `--allow-public` + 32-char `--token` flag. Otherwise `RunE` returns a wrapped sentinel error.
21. **T5.2** Server middleware: if `--token` set, every request must carry `Authorization: Bearer <token>` or 401.
22. **T5.3** Tests cover: bind `0.0.0.0:8787` alone → error; bind `0.0.0.0:8787 --allow-public --token=x...` → starts; missing token on request → 401.
23. **T5.4** Commit "feat(ui): require explicit consent + token for public bind".

---

## 4. Files likely to change

```
cmd/
├── ui.go                      # add --allow-public, --token, --tui flags
├── ui_test.go                 # +3 tests for flag combinations
ui/
├── server.go                  # new routes: /graph, /commands/plan
├── server_test.go             # new (Phase 1)
├── parse/
│   ├── project.go             # new
│   ├── project_test.go        # new
│   └── testdata/proj-fixture/ # new
├── watch/
│   ├── watcher.go             # new
│   └── watcher_test.go        # new
├── runstore.go                # add cap + ErrStoreFull
├── runstore_test.go           # new (Phase 1)
├── templates/                 # existing
└── static/                    # existing
go.mod / go.sum                # if Phase 3 (fsnotify) or Phase 6 (bubbletea) lands
```

---

## 5. Tests / validation

| Layer | Tool | Target |
|---|---|---|
| Unit | `go test ./...` | All packages must keep green |
| Race | `go test -race ./ui/...` | Run-store + watcher must be race-free |
| HTTP | `httptest.NewServer` per route | Each new route gets one happy + one 4xx test |
| E2E | `make`-style shell script in `scripts/`: spawn binary, `curl /healthz`, `curl /`, assert body contains expected aggregate names | Manual smoke before each PR merge |
| Security | `grep -rn 'os/exec' ui/` | The exec call site stays in exactly one file (`run.go`) — no silent spread |

Pre-merge gate per phase:

- `go test ./...` exits 0
- `go test -race ./ui/...` exits 0
- No new top-level deps without an entry in this plan

---

## 6. Risks, tradeoffs, open questions

| Risk | Severity | Mitigation |
|---|---|---|
| html/template silently swallows parse errors → blank page | high (already burned us) | T1.1 + every `parseTemplates` call must `_ = tmpl.Execute` and assert in test |
| Local-bind to `0.0.0.0` exposes run-capable UI on LAN | high | Phase 5 hardens; until then, refuse non-loopback binds |
| Generator coupling — UI depends on generator's internal AST | medium | Land Phase 4 last to avoid early rework |
| `fsnotify` flakes on macOS | low | Polish-level: stay with polling fallback until proven flaky |
| Process runner hangs on a misbehaving `esb` command | medium | Already has 5s shutdown ctx; add per-run 60s timeout + user-visible "cancel" button (deferred to Phase 6) |

**Open question for user:**

- Is idea G (diff preview) a Phase-2 priority or can it wait until Phase 4? It is high-value but it forces an internal-API refactor of the generators. If the user wants to ship the graph view + hot-reload first, G becomes Phase 4.

---

## 7. Out of scope (YAGNI)

- Multi-project switching (one `esb ui` per project is fine).
- Login flows / persistent sessions — local-only by definition.
- Editor integration (LSP, vscode extension) — different toolchain.
- WebSocket streaming of run output — polling at 1s is enough for the 5–10s commands `esb` emits; revisit if user complains.

---

## 8. Verification checklist before marking "done"

- [ ] Each phase has its own commit with tests
- [ ] `go test ./... -count=1` 0 failures
- [ ] Manual: `cd testdata/proj-fixture && esb ui --no-open` → `curl http://127.0.0.1:8787/healthz` returns `200 ok` and `curl http://127.0.0.1:8787/` renders `/aggregates/order`
- [ ] No new external dep without an entry above
- [ ] No additional commits after this plan artifact on the branch
- [ ] Branch contains at least one full Phase (e.g. Phase 1 commit) before requesting review
