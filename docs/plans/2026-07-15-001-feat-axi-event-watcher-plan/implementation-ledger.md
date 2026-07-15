# Implementierungsledger

| ID | Target | Source class | Intended action | Status | Evidence | Notes |
|---|---|---|---|---|---|---|
| L1 | `internal/cli/axi.go`, `internal/cli/axi_watch.go` | plan U1 | create/update | verified | `go test ./internal/cli`; `./bin/no-mistakes axi watch --help` | Explizite Run-ID, zwei `--until`-Modi, read-only Daemon-Anschluss |
| L2 | `internal/ipc/client.go` | plan U2a | update | verified | `TestSubscribeContextCancelsDuringHandshake` | Kontextabbruch schließt nur die Beobachtungsverbindung |
| L3 | `internal/cli/axi_watch_test.go`, `internal/ipc/subscribe_test.go` | plan U1-U3/U2a | create/update | verified | Parse-, Quiet-, Output- und Handshake-Regressionen | TDD: fehlende Symbole zuerst rot, danach grün |
| L4 | `internal/skill/skill.go`, `skills/no-mistakes/SKILL.md`, `docs/src/content/docs/**` | plan U4 | update/verify | verified | `make skill`; `make lint`; Docs-Build | Foreground-Watch und opt-in Supervisor klar getrennt |
| L5 | `internal/e2e/axi_journey_test.go` oder Daemon-Harness | plan U5 | update/verify | partial | `make e2e` Timing-Flake; `TestUserJourney/claude` grün; CLI-Help-Smoke | Bestehende E2E-Suite abgedeckt, aber kein Watch-spezifischer Daemonfall |
| L6 | `internal/cli/axi_supervise.go`, `internal/supervision/**` | plan U6 | create | verified_local | Store- und Hook-Parser-Tests | Opt-in Registrierung, Arbeitskopie-Bindung und Stop-Hook-Parser |
| L7 | `internal/supervision/**`, `internal/cli/axi_supervise_test.go`, Skill-/Docs-Flächen | plan U7 | create/update | verified_local | Fokussierte CLI-/Supervisor-Tests, Windows-Kompilierung | Worker, Resume-Deduplizierung, Status und CEO-Handoff; Live-Hook offen |
