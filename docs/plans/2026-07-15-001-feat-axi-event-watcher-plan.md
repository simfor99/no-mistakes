---
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# AXI-Ereignisbeobachter - Plan

## Zusammenfassung

`no-mistakes axi watch` ergänzt die vorhandene AXI-Oberfläche um eine
ereignisgesteuerte Beobachtung eines **explizit angegebenen** Runs. Der Befehl
nutzt den bestehenden daemonseitigen Run-Eventstream und liefert eine begrenzte
TOON-Zustandsaufnahme zurück, sobald externe Aufmerksamkeit nötig ist oder der
Run endet. Er startet, beantwortet, repariert, rebased, wiederholt oder bricht
keinen Run ab.

Der Watcher ist der Ereignissensor eines opt-in Codex-CLI-Supervisors. Endet
ein Codex-Turn, registriert ein explizit installierter Codex-Stop-Hook dessen
Session-ID und startet genau einen getrennten Watch-Worker. Bei einer echten
Zustandsänderung setzt der Worker dieselbe gespeicherte Session mit
`codex exec resume` fort. Nur ein echtes CEO-Gate stoppt diese Kette und
benachrichtigt Simon.

## Problemrahmen

`axi run` und `axi respond` warten bereits synchron bis zum nächsten Gate,
`checks-passed` oder Endzustand. Für einen bereits laufenden Run gibt es aber
nur wiederholtes `axi status`-Polling. Wenn Smart Commit den Aufruf in den
Hintergrund legt, verliert die aktive Codex-Runde dessen Abschluss und kann
den Lauf nicht autonom weiterführen.

Die vorhandene IPC-Subscription ist die richtige Grundlage. Sie wird von der
TUI verwendet, ist aber noch nicht als AXI-Contract für Agent-Supervisoren
angeboten. Codex CLI liefert keinen nativen Wake-up-Contract für beendete
Turns; ohne getrennten Supervisor kann No-Mistakes den aufrufenden Agenten
nicht selbst wecken.

## Produktvertrag

### Produktintention

Ein explizit aktivierter Codex-CLI-Supervisor soll einen laufenden
No-Mistakes-Run ohne Status-Polling bis zum terminalen Zustand begleiten und
dieselbe Codex-Session nur bei einer echten Run-Änderung fortsetzen. Der
Supervisor beantwortet keine Gates selbst und darf keine CEO-Entscheidung
umgehen.

### User Story

Als Nutzer eines agentengesteuerten Codex-CLI-Laufs möchte ich einen bekannten
Run ereignisgesteuert überwachen lassen, damit Codex technische Arbeit selbst
weiterführt und mich nur bei einer echten Entscheidung informiert, statt dass
ich regelmäßig nach dem Status fragen muss.

### Ziel und Nutzerreise

Smart Commit kennt die Run-ID und aktiviert `axi supervise arm --run <id>`.
Der Codex-Stop-Hook erhält beim Ende des aktuellen Turns die Session-ID,
übernimmt ausschließlich diese gerüstete Run-ID und startet einen getrennten
Watch-Worker. Dieser ruft `axi watch --until attention` auf, speichert den
beobachteten Stoppgrund dauerhaft und setzt die gleiche Session genau einmal
mit einem begrenzten Ereignis-Prompt fort. Die fortgesetzte Runde entscheidet
innerhalb des bestehenden Smart-Commit-Rahmens über technische Antworten und
ruft gegebenenfalls `axi respond` auf.

Nach einem fortgesetzten Turn prüft der Stop-Hook autoritativ: Läuft der Run
wieder, beginnt eine neue Watch-Phase; steht er an einem CEO-Gate, wird die
Supervision auf `awaiting_user` gesetzt und Simon über eine lokale
Benachrichtigung informiert. Die automatische Kette pausiert, bis dieselbe
Session Simons Antwort erhalten hat und danach erneut endet; erst dann darf die
nächste Watch-Phase beginnen. Ist der Run terminal, wird die Registrierung als
abgeschlossen markiert. Quiet ist kein Simon-Gate und löst höchstens eine
begrenzte Wiederaufnahme zur Liveness-Prüfung aus.

`--until terminal` bleibt eine rein beobachtende Variante. Sie gibt nur den
terminalen Snapshot aus und beobachtet Gate, `checks-passed` und Quiet ohne
Zwischen-Ausgabe weiter. Das ist nicht der Codex-Supervisionsmodus.

### Admin-/Operator-Journey

Nicht anwendbar: Der Change ergänzt nur den agentenseitigen Read-Contract und
schafft keine neue Bedienoberfläche, Rolle oder Administrationsaktion.

### Anforderungen

- R1: `axi watch` verlangt immer `--run <id>` und fällt niemals still auf
  aktuellen Branch oder letzten Run zurück.
- R2: `--until attention` ist der Default und beendet die Beobachtung bei
  Gate, `checks-passed`, Quiet-Signal oder terminalem Runzustand.
- R3: `--until terminal` beobachtet durch Gate, `checks-passed` und Quiet
  hindurch, ohne den Run zu verändern oder Zwischen-Snapshots auszugeben, bis
  zum terminalen Zustand.
- R4: Jeder erfolgreiche Beobachtungsabschluss enthält einen begrenzten TOON-Snapshot mit eindeutigem
  Stoppgrund und der Grenze `supervision: active_agent_required`; ein Gate enthält
  zusätzlich eine für den Watcher begrenzte Gate-Darstellung und ihre
  Handlungsanweisungen.
- R5: Stream-Ereignisse sind nur Aufwecker. Vor jeder Entscheidung wird der
  aktuelle, autoritative Runzustand erneut geladen, damit verlorene,
  verspätete oder lückenhafte Events keinen falschen Zustand behaupten.
- R6: SIGINT, SIGTERM und Kontextabbruch trennen ausschließlich die
  Beobachtung, geben einen begrenzten `interrupted`-Snapshot mit Exitcode 130
  aus und senden niemals `axi abort` oder eine andere Mutation. Ein
  nicht-terminal geschlossener Stream wird nach dem finalen Read als
  `stream-interrupted` mit Exitcode 1 ausgegeben; schlägt dieser finale Read
  fehl, gilt der bestehende strukturierte AXI-Betriebsfehler.
- R7: Der Watcher bleibt datensparsam: kein Rohlog, Diff, Prompt oder Event-
  Content auf stdout oder in Telemetrie. Er folgt dem bestehenden Read-Surface-
  Telemetrie-Gate ohne Pageview.
- R8: Skill, punktnahe AXI-Hilfe und Agent-Dokumentation erklären dieselbe
  Grenze: `axi watch` ist nur der Read-only-Ereignissensor; echte autonome
  Fortsetzung verlangt den opt-in Codex-Supervisor.
- R9: `axi supervise arm --run <id>` registriert exakt einen aktiven Run für
  die aktuelle Arbeitskopie, enthält keine Session-ID und startet keinen
  Worker. Es darf keine Pipeline-Mutation auslösen.
- R10: `axi codex-hook` akzeptiert ausschließlich Codex-Stop-Hook-JSON über
  stdin, übernimmt atomar nur eine gerüstete Registrierung derselben
  Arbeitskopie und startet höchstens einen entkoppelten Worker.
- R11: Der Worker hält einen pro Registrierung exklusiven Lock, beobachtet
  ausschließlich die registrierte Run-ID und setzt höchstens einen ruhenden
  Codex-Thread für jedes neue Ereignis fort. Fehlgeschlagene Resumes und
  fehlende `codex`-Binaries bleiben als begrenzter lokaler Zustand sichtbar;
  sie starten keine Retry-Stürme.
- R12: Ein geparktes `ask-user`-/CEO-Gate pausiert automatische Resumes,
  markiert die Registrierung `awaiting_user` und löst einen lokalen Hinweis
  aus. Nur dieselbe Session darf nach Simons Antwort und einem späteren
  Turn-Ende die nächste Watch-Phase anfügen. Ein terminaler Run wird als
  abgeschlossen markiert.
- R13: Hook-Installation und lokale Benachrichtigung bleiben explizites Opt-in;
  ein No-Mistakes-Update darf weder `~/.codex/hooks.json` noch globale
  Benachrichtigungs- oder Codex-Settings still ändern.

### Nicht im Umfang

- Kein globaler Autostart, Cron-Job oder stiller Eingriff in Codex-Settings.
- Keine automatische Gate-Antwort außerhalb des bestehenden, ausdrücklich
  zustimmungsgebundenen `--yes`-Pfads.
- Keine neue Daemon- oder Datenbank-Persistenz und keine neue menschenorientierte
  No-Mistakes-TUI.
- Keine Zusage, dass ein nicht installierter oder nicht vertrauenswürdiger Hook
  einen Codex-Turn fortsetzt.

## Technische Entscheidungen

| Entscheidung | Begründung |
| --- | --- |
| Bestehendes `ipc.Subscribe` wiederverwenden | Der Daemon streamt bereits run-scoped Ereignisse; ein zweiter Polling- oder Daemonvertrag wäre unnötige Doppelarbeit. |
| Event als Aufwecker, erneuter Run-Read als Wahrheit | Subscriptions enthalten keine Startaufnahme und können bei langsamen Konsumenten Events verlieren. |
| Explizite Run-ID | Ein Supervisor darf niemals versehentlich einen anderen oder den letzten Run beobachten. |
| `attention` als Default | Gates verlangen externe Entscheidung; ein ausschließliches `--until-terminal` würde einen wartenden Run wie autonome Arbeit wirken lassen. |
| Quiet als Hinweis, nicht als Fehler | Quiet bedeutet fehlende Aktivität, nicht Berechtigung zum Abbruch, Re-run oder zur Reparatur. |
| Read-only bedeutet auch keinen Daemon-Start | `watch` abonniert nur einen bereits laufenden Daemon. Er darf weder `EnsureDaemon` noch Stale-Recovery, Resume oder Worktree-Cleanup auslösen. |
| Terminal-Modus hat keinen Zwischen-Stream | Ein einzelner terminaler Snapshot vermeidet einen zweiten Streaming-Parser und bleibt als passive Diagnose bewusst klein. |
| Codex-Session-ID kommt nur aus dem Stop-Hook | Die laufende AXI-CLI darf keine Session-Dateien erraten oder eine fremde Codex-Session übernehmen. |
| Registrierung vor Worker | `arm` schreibt nur eine kleinste lokale Registrierung; erst ein verifizierter Stop-Hook darf sie mit seiner Session-ID übernehmen. |
| Ereignis statt Zeitplan | Der Worker wacht auf AXI-Events und einer einzelnen Quiet-Deadline, nicht über einen Status-Cron. |
| CEO-Gate pausiert die Automatik | Der Worker darf nicht resume'n, solange der Run auf Simons Antwort wartet; dieselbe Session kann sich danach wieder anhängen. |

## Hochrangiges Design

```text
Smart Commit / active Codex turn
        |
        +--> axi supervise arm --run <id>
        |
        v
Codex Stop-Hook receives session_id
        |
        +--> atomically claim matching armed registration
        v
detached worker: axi watch --run <id> --until attention
        |
        +--> authoritative snapshot via IPC + Quiet-Deadline
        |
        +--> resume same Codex session once
                    |
                    +--> active run: arm next worker on Stop
                    +--> CEO gate: awaiting_user + local notification
                    +--> terminal: finalize registration
```

## Implementierungseinheiten

### U1. Watch-Contract und CLI-Einstieg

**Ziel:** Den read-only AXI-Befehl mit verpflichtender Run-ID und valider
`--until`-Auswahl registrieren.

**Requirements:** R1, R2, R3, R7

**Dependencies:** Keine.

**Files:** `internal/cli/axi.go`, `internal/cli/axi_watch.go`,
`internal/cli/axi_watch_test.go`

**Approach:** `axi watch` als Read Surface registrieren. Nur `attention` und
`terminal` akzeptieren; unbekannte Werte liefern den bestehenden strukturierten
Usage-Fehler. Die Telemetrie bleibt auf Zustandsfingerprints begrenzt.

**Patterns to follow:** `internal/cli/axi_query.go`,
`internal/cli/telemetry.go`, `internal/cli/axi_drive.go`.

**Test scenarios:**

- Ohne `--run` entsteht ein strukturierter Usage-Fehler; kein Branch-Fallback.
- `attention` ist der Default, `terminal` wird akzeptiert und ein anderer Wert
  wird als Usage-Fehler abgewiesen.
- Der Befehl erzeugt keinen Pageview und trägt weder Run-ID noch Rohdaten in
  die Telemetrie.

**Verification:** CLI-Hilfe und Unit-Tests beweisen den öffentlichen
Flag-Contract (`unit_mocked`).

### U2. Autoritative ereignisgesteuerte Beobachtung

**Ziel:** Einen Run ohne Polling-Dauerschleife bis zum passenden Stoppgrund
beobachten.

**Requirements:** R2, R3, R5, R6

**Dependencies:** U1.

**Files:** `internal/cli/axi_watch.go`, `internal/cli/axi_watch_test.go`

**Approach:** Ohne `EnsureDaemon` zuerst den persistierten Run lesen, bei
terminalem Run direkt rendern und bei aktivem Run ausschließlich einen bereits
erreichbaren Daemon abonnieren. Vor und nach der Subscription sowie nach jedem
Event wird der autoritative Runzustand gelesen. Ein monotonicer Timer wird nach
jedem Read bis zur nächsten Quiet-Grenze gesetzt; sein Ablauf erzwingt genau
einen erneuten Read, statt eine Polling-Schleife zu starten. Bestehende
`runView`, `ciReadyToMerge` und Quiet-Semantik bestimmen den Stoppgrund. Ein
geschlossener Stream wird immer gegen einen letzten Read abgeglichen: terminal
wird ehrlich gerendert, sonst wird `stream-interrupted` gemeldet und der Run
unangetastet gelassen. Kann auch der finale Read nicht erfolgen, liefert AXI
den bestehenden strukturierten Betriebsfehler. Im Terminal-Modus latcht ein
festgestelltes Quiet bis zu einem neuen Stream-Ereignis; dieselbe alte
Aktivitätszeit darf keinen sofort erneut fälligen Timer erzeugen.

**Patterns to follow:** `internal/ipc/client.go`,
`internal/daemon/manager.go`, `internal/cli/axi_drive.go`.

**Test scenarios:**

- Bereits geparktes Gate endet in `attention` sofort ohne IPC-Mutation.
- Gate zwischen erster Aufnahme und Subscription wird durch die zweite
  Aufnahme erkannt.
- Ein aktivierter Stream führt bei Gate, CI-Bereitschaft und terminalem Zustand
  zum richtigen Stoppgrund.
- Keine Stream-Ereignisse bis zur Quiet-Deadline führen nach dem Timer-Read zu
  `attention: quiet`; frische Aktivität vor der Deadline verschiebt sie.
- Timer-Reset, deaktivierte Quiet-Schwelle und Race zwischen Timer und Event
  werden deterministisch abgedeckt.
- Ein bereits quietter, weiterhin aktiver Terminal-Run setzt ohne neues Event
  keinen weiteren Quiet-Read in Gang.
- `terminal` beobachtet Gate, Quiet und `checks-passed` ohne Zwischen-Output
  weiter; `attention` kehrt jeweils zurück.
- Geschlossener Stream mit weiterhin aktivem Run liefert `stream-interrupted`;
  geschlossener Stream mit terminalem Run liefert dessen Ergebnis; ein
  fehlgeschlagener finaler Read liefert den strukturierten Betriebsfehler.
- Ein nicht laufender oder nicht erreichbarer Daemon liefert einen read-only
  Fehler und startet weder Recovery noch Resume.
- Kontextabbruch, SIGINT und SIGTERM beenden nur die Subscription, geben den
  definierten `interrupted`-Snapshot aus und lösen keinen Cancel-IPC-Aufruf aus.

**Verification:** Deterministische CLI-/IPC-Tests (`integration_local`) zeigen
die Beobachtungs- und Nichtmutationsgrenze. Sie beweisen keine automatische
Codex-Fortsetzung.

### U2a. Unterbrechbare Subscription-Verbindung

**Ziel:** Schon der Handshake einer Watch-Subscription reagiert auf
Kontextabbruch und kann den Watch-Prozess nicht festhalten.

**Requirements:** R6

**Dependencies:** U1.

**Files:** `internal/ipc/client.go`, `internal/ipc/subscribe_test.go`,
`internal/cli/axi_watch.go`, `internal/cli/axi_watch_test.go`

**Approach:** Eine kontextfähige Subscription-Variante begrenzt den Handshake
und schließt ihre Verbindung bei Kontextende. `axi watch` leitet aus
SIGINT/SIGTERM einen eigenen Notify-Context ab, damit der laufende Pipeline-Run
nicht abgebrochen wird und der definierte `interrupted`-Snapshot entsteht.

**Patterns to follow:** `internal/daemon/daemon.go` für Signal-Kontexte,
`internal/ipc/client.go` für Connection-Cleanup.

**Test scenarios:**

- Ein Daemon, der den Subscribe-Handshake nicht beantwortet, wird durch
  Kontextabbruch beendet und hinterlässt keine blockierte Verbindung.
- SIGINT und SIGTERM während Handshake und Stream führen zu Exit 130 und keinem
  `CancelRun`-IPC-Aufruf.

**Verification:** IPC- und Subprocess-Harness liefern `integration_local`-
Evidenz für die lokale Unterbrechbarkeit; sie verändern keinen Pipeline-Run.

### U3. Begrenzte TOON-Rückgabe und Ausstiegssemantik

**Ziel:** Jeder Beobachtungsabschluss ist für Agenten eindeutig lesbar und
behält die etablierten AXI-Exitcodes bei.

**Requirements:** R4, R6, R7

**Dependencies:** U2, U2a.

**Files:** `internal/cli/axi_watch.go`, `internal/cli/axi_watch_test.go`

**Approach:** TOON enthält einen Watch-Block mit Stoppgrund, Terminalstatus,
`supervision: active_agent_required` und `auto_resumed: false`. Watch-Gates nutzen
einen eigenen begrenzten Renderer mit sichtbarer Gesamtzahl und
Trunkierungsmarker; gespeicherte Fehler und Zusammenfassungen sind ebenfalls
begrenzt. Terminale Ausgaben nutzen die bestehenden Outcome-Regeln. Gate, Quiet
und `checks-passed` sind beobachtete Ergebnisse mit Exitcode 0;
failed/cancelled und `stream-interrupted` bleiben nicht-null; SIGINT/SIGTERM
enden mit 130.

**Patterns to follow:** `internal/cli/axi_render.go`,
`internal/cli/axi_drive.go`.

**Test scenarios:**

- Gate enthält Watch-Grenze und bestehende Gate-Hilfe, aber keine automatische
  Antwortbehauptung.
- `checks-passed` bleibt von `passed` unterscheidbar.
- Failed und cancelled geben den gespeicherten Fehler samt terminalem
  Stoppgrund aus und enden nicht erfolgreich.
- Viele Findings und ein langer gespeicherter Fehler bleiben begrenzt und
  markieren ihre Trunkierung sichtbar.
- Rohinhalt der IPC-Events, Diffs und Logs erscheint nicht im TOON-Output.

**Verification:** Format- und Exitcode-Tests (`unit_mocked`) sichern den
maschinellen Contract; sie beweisen keine Scheduler-Integration.

### U4. Dokumentierte aktive Agentenrunde

**Ziel:** Installierte Agent-Skills und veröffentlichte Dokumentation erklären
den Watcher einheitlich als Read-only-Baustein des opt-in Codex-Supervisors.

**Requirements:** R8

**Dependencies:** U1, U2, U2a, U3.

**Files:** `internal/skill/skill.go`, `skills/no-mistakes/SKILL.md`,
`docs/src/content/docs/guides/agents.md`,
`docs/src/content/docs/reference/cli.md`,
`internal/cli/axi_guidance.go`, `internal/cli/axi_guidance_test.go`

**Approach:** Die generierte Skillquelle wird zuerst geändert, dann mit
`make skill` gerendert. Live-Hilfe und Agent-Guide enthalten dieselbe
Invariante: Ein vordergründiger Watch hilft nur innerhalb eines lebenden Turns;
für autonome CLI-Begleitung wird der opt-in Supervisor gerüstet und sein Hook
explizit installiert.

**Patterns to follow:** `AGENTS.md` Abschnitt „Agent-Guidance Surfaces“ und
`internal/cli/axi_guidance_test.go`.

**Test scenarios:**

- Die drei Guidance-Flächen trennen vordergründigen Watch und opt-in
  Supervisor, erklären dessen Hook-Grenze und behaupten keine Autonomie ohne
  installierten Hook.
- `make skill` erzeugt keinen Drift in `skills/no-mistakes/SKILL.md`.

**Verification:** Guidance-Synchronisation und generierter-Skill-Check
(`integration_local`) beweisen Dokumentationskonsistenz, nicht die
Scheduler-Ausführung.

### U5. End-to-End-Beobachtung am echten Daemon-Harness

**Ziel:** Die neue Oberfläche über den vorhandenen Daemon- und Subscription-
Pfad absichern.

**Requirements:** R1 bis R8

**Dependencies:** U1 bis U4 inklusive U2a.

**Files:** `internal/e2e/axi_journey_test.go` oder
`internal/daemon/subscribe_recover_test.go`

**Approach:** Den bestehenden Harness für einen Run mit Gate und einen
abgeschlossenen Run erweitern. Die Tests verwenden die reale lokale
IPC-Subscription und den Test-Daemon, aber keine reale Codex-Session.

**Test scenarios:**

- Ein Gate-Run liefert `attention` und bleibt geparkt, bis eine getrennte,
  explizite `axi respond`-Aktion folgt.
- Ein terminaler Run liefert sein finales Outcome.
- Ein Prozess-SIGINT und -SIGTERM liefert den definierten Exitcode, beendet nur
  die Beobachtung und lässt den Daemon-Run aktiv.
- Unterbrochene Beobachtung beeinflusst den Run nicht.

**Verification:** `make e2e` liefert `integration_local`-Evidenz für CLI,
Daemon und IPC. Es beweist nicht, dass Codex CLI oder ein ChatGPT-Scheduler
automatisch eine neue Runde startet.

### U6. Opt-in Supervisor-Registrierung und Codex-Stop-Hook

**Ziel:** Den aktiven Run sicher an einen späteren Stop-Hook derselben
Arbeitskopie binden, ohne Codex-Session-Dateien zu lesen oder globale Hooks zu
installieren.

**Requirements:** R9, R10, R13

**Dependencies:** U1 bis U3.

**Files:** `internal/cli/axi_supervise.go`,
`internal/cli/axi_supervise_test.go`, `internal/supervision/**`.

**Approach:** `axi supervise arm --run <id>` validiert den bekannten aktiven
Run und schreibt eine atomare, eng berechtigte Registrierung unter dem
No-Mistakes-State-Root. `axi codex-hook` liest genau ein Codex-Hook-JSON von
stdin, akzeptiert nur `Stop` samt nichtleerem `session_id` und gleichem `cwd`,
und übernimmt höchstens eine gerüstete Registrierung. Die CLI installiert oder
ändert keinen Codex-Hook; die Dokumentation zeigt die manuelle,
vertrauenspflichtige Hook-Definition.

**Test scenarios:**

- Arm ohne explizite Run-ID oder mit terminalem/fremdem Run schlägt ohne
  Registrierung fehl.
- Ein fremdes Hook-Event, fehlende Session-ID oder anderer `cwd` kann keine
  Registrierung übernehmen.
- Gleichzeitige Stop-Hooks können nur einmal claimen.
- Eine vorhandene `awaiting_user`-Registrierung kann weder von einer fremden
  Session noch ohne Simons Antwort reaktiviert werden; eine terminale
  Registrierung wird nie neu aktiviert.

**Verification:** Zustands- und Hook-Parser-Tests (`unit_mocked`) belegen die
Zuordnung und den Opt-in-Contract.

### U7. Entkoppelter Ereignis-Worker, Resume und Nutzer-Handoff

**Ziel:** Nach einem regulären Codex-Turn-Ende denselben Thread bei genau einem
relevanten AXI-Ereignis fortsetzen und bei CEO-Gates sicher an Simon übergeben.

**Requirements:** R10, R11, R12

**Dependencies:** U2, U3, U6.

**Files:** `internal/supervision/**`, `internal/cli/axi_supervise.go`,
`internal/cli/axi_supervise_test.go`, `docs/src/content/docs/guides/agents.md`,
`internal/skill/skill.go`.

**Approach:** Der vom Hook getrennt gestartete Worker besitzt einen
registrierungslokalen Lock und verwendet `axi watch --until attention` als
einzigen Event-Sensor. Vor einem Resume persistiert er einen monotonen
Ereignisfingerprint und `handoff_in_progress`; erst danach ruft er den
installierten `codex exec -C <cwd> resume <session-id> <bounded-prompt>` auf.
Nach einem späteren Stop entscheidet ein autoritativer Run-Read zwischen
nächster Watch-Phase, `awaiting_user`, terminalem Aufräumen und klarer
`resume_failed`-Diagnose. Benachrichtigungen sind best-effort und dürfen den
Zustandsübergang nicht verdecken.

**Test scenarios:**

- Ein Ereignis startet genau ein Resume, auch wenn Hook und Stream mehrfach
  eintreffen.
- Ein aktiver Run nach dem Resume startet erst beim folgenden Stop einen neuen
  Worker.
- Ein geparktes `ask-user`-Gate geht nach genau einem Handoff auf
  `awaiting_user`, startet vor Simons Antwort keinen weiteren Resume-Versuch
  und hängt sich erst nach einem späteren Ende derselben Session wieder an.
- Fehlendes Codex-Binary, nicht-null Resume-Exit und Worker-Crash bleiben
  dauerhaft lesbar, ohne Schleife oder Pipeline-Mutation.
- Terminales Ergebnis räumt den Worker-Lock auf und markiert die Registrierung
  als abgeschlossen; ein Neustart kann keinen alten Run wiederbeleben.

**Verification:** Unit- und lokaler Worker-Harness (`integration_local`)
belegen Deduplication, Resume-Aufruf und CEO-Stopp. Ein echter opt-in
Codex-CLI-Run mit Hook liefert zusätzlich `live_local`-Evidenz.

## Fallmatrix und Qualitätsgates

| Fall | Erwartung | Evidenzklasse | Gate |
| --- | --- | --- | --- |
| Bereits geparktes Gate | `attention: gate`, keine Mutation | integration_local | erforderlich |
| Gate-Race beim Start | Zweite Aufnahme erkennt das Gate | integration_local | erforderlich |
| CI grün, PR offen | `checks-passed`, nicht `passed` | integration_local | erforderlich |
| Quiet | Hinweis, keine Reparatur oder Abbruchaktion | unit_mocked | erforderlich |
| Verlorenes Event bei offenem Stream | Quiet-Deadline löst autoritativen Read aus, kein endloses Warten | integration_local | erforderlich |
| Bereits quietter Terminal-Run | Quiet-Latch verhindert hektische Reads bis zu einem neuen Event | integration_local | erforderlich |
| Erfolgreich terminal | `passed`, Exit 0 | integration_local | erforderlich |
| Failed/cancelled | Persistierter Fehler, nicht-null Exit | integration_local | erforderlich |
| Stream-Abbruch | Letzter Read oder `stream-interrupted` Exit 1; bei fehlendem Read strukturierter Fehler | integration_local | erforderlich |
| SIGINT/SIGTERM | `interrupted`, Exit 130, Subscription endet, Pipeline läuft weiter | integration_local | erforderlich |
| Hängender Subscribe-Handshake | Kontext löst die Verbindung, kein blockierter Watch-Prozess | integration_local | erforderlich |
| Daemon nicht erreichbar | Strukturierter Fehler ohne Start, Recovery oder Resume | integration_local | erforderlich |
| Fremder oder paralleler Stop-Hook | Keine Übernahme der Registrierung | unit_mocked | erforderlich |
| Doppelte Ereignisse | Genau ein Resume mit Fingerprint und Lock | integration_local | erforderlich |
| CEO-Gate nach Resume | `awaiting_user`, lokaler Hinweis, kein Resume vor Simons Antwort; dieselbe Session kann sich danach wieder anhängen | integration_local | erforderlich |
| Fehlgeschlagenes Resume | Dauerhafte Diagnose, begrenzter Retry, keine Pipeline-Mutation | integration_local | erforderlich |
| Echte Codex-Wiederaufnahme | Gleiche Session wird nach AXI-Ereignis fortgesetzt | live_local | erforderlich |

## Risiko und Begrenzung

- Ein Stream kann Ereignisse verlieren oder ohne terminales Ereignis enden.
  Die Quiet-Deadline verhindert endloses Warten bei offenem Stream; der finale
  autoritative Read verhindert eine falsche Erfolgsbehauptung bei Stream-Ende.
- `--until terminal` kann bei einem Gate absichtlich lange warten. Seine Hilfe
  muss deshalb die externe Supervisor-Pflicht ausdrücklich nennen.
- Der Codex-Hook ist ein lokaler Vertrauensanker. Er darf erst nach sichtbarer
  Installation und Hash-Prüfung laufen und muss sich bei unbekanntem Input
  wirkungslos verhalten.
- Ein Resume kann eine gespeicherte CLI-Session fortsetzen, garantiert aber
  nicht, dass eine bereits offene TUI deren neue Ausgabe live rendert. CEO-Gates
  brauchen deshalb zusätzlich einen lokalen Hinweis.

## Quellen und Forschung

- `internal/ipc/protocol.go`, `internal/ipc/client.go`,
  `internal/daemon/daemon.go`: vorhandener run-scoped Subscription-Contract.
- `internal/cli/axi_drive.go`, `internal/cli/axi_query.go`:
  bestehende Gate-, CI- und Outcome-Semantik.
- `internal/daemon/manager.go`: Subscriptions haben keine Startaufnahme und
  können bei langsamen Empfängern Ereignisse verlieren.
- [No-Mistakes CLI-Referenz](https://kunchenguid.github.io/no-mistakes/reference/cli/).
- [OpenAI Scheduled tasks](https://learn.chatgpt.com/docs/automations.md) und
  [Long-running work](https://learn.chatgpt.com/docs/long-running-work.md):
  die Codex-CLI verwaltet keine geplante Fortsetzung selbst.

## Definition of Done

- `axi watch --run <id>` erfüllt R1 bis R8 und hat keinen mutierenden
  Nebeneffekt.
- Der opt-in Supervisor erfüllt R9 bis R13: pro Run genau ein Worker/Resume,
  keine fremde Session, kein Resume vor einer CEO-Antwort und klare lokale
  Fehlerdiagnosen.
- Unit-, Race-/Stream- und lokale Daemon-E2E-Fallmatrix ist grün.
- Skill, Live-Hilfe und Dokumentation sind synchron generiert.
- `make lint`, `go test -race ./...`, `make e2e` und
  `go build -o ./bin/no-mistakes ./cmd/no-mistakes` bestehen.
- Die Abschlussmeldung benennt die Evidenzklasse und trennt lokale Harness-
  Evidenz von einem echten Codex-CLI-Hook-Resume.

## Implementierungsstand

- **U1 bis U4:** lokal umgesetzt und durch fokussierte CLI-, IPC- und
  Renderer-Tests sowie `make skill` geprüft.
- **U5:** Der bestehende lokale E2E-Harness wurde ausgeführt. Sein erster Lauf
  zeigte einen zeitabhängigen Zwischenstands-Flake außerhalb dieses Changes;
  die gezielte Wiederholung `TestUserJourney/claude` war grün. Der Harness
  beweist noch keinen `axi watch`-spezifischen Daemonfall.
- **U6 bis U7:** lokal umgesetzt: run- und arbeitskopiegebundene Registrierung,
  Stop-Hook-Parser, exklusiver Worker-Lock, einmaliges Resume, sichtbarer
  lokaler Status und `awaiting_user`-Handoff. Unit- und Plattform-Kompilierung
  sind grün.
- **Offen für `live_local`:** Ein echter, von Simon überprüfter Eintrag in
  `~/.codex/hooks.json` und ein kurzlebiger Codex-CLI-Run müssen den
  Stop-Hook, Worker und dieselbe Session-Wiederaufnahme beweisen. Diese
  persönliche Konfigurationsänderung wird weder automatisch installiert noch
  als bereits geprüft behauptet.

## Entscheidungs- und Annahmenledger

- **Entscheidung:** `attention` ist der Default, `terminal` ist passiv.
  **Quelle:** bestehende Gate-Semantik und User-Ziel. **Status:** entschieden;
  verhindert einen irreführenden Autonomie-Contract.
- **Entscheidung:** Der externe Teil ist ein expliziter Codex-Stop-Hook plus
  entkoppelter Worker, keine heimliche Cron-Aufgabe. **Status:** entschieden;
  Nutzerentscheidung Option 1 am 2026-07-15.
- **Entscheidung:** Kein neuer Daemon- oder Datenbankvertrag. **Status:**
  entschieden; der vorhandene Eventstream deckt den Bedarf.
- **Befund:** No-Mistakes 1.37 änderte gegenüber 1.36 keinen parentseitigen
  Wake-up-Contract. Codex CLI besitzt dafür keinen nativen Mechanismus;
  Hintergrundabschlüsse wecken einen beendeten Turn nicht zuverlässig.
  **Status:** durch Release-Diff und Codex-Dokumentation/-Issues belegt.

## Elons Principles Guard

Anforderung geprüft: Ein Watcher allein ist nicht genug; ein minimaler,
opt-in Session-Adapter ist notwendig. Vermieden: Cron/Autostart, eine zweite
Polling-Implementierung, Session-Datei-Raten und eine globale Hook-Installation.
Vereinfacht vor Automatisierung: vorhandenen IPC-Stream als Wecksignal nutzen,
Codex-Session-ID ausschließlich vom offiziellen Hook erhalten. Automatisierung
endet sichtbar an CEO-Gates.
