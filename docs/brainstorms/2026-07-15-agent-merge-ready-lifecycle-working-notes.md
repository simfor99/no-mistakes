# Agenten-Lebenszyklus „merge_ready“ – Arbeitsnotizen

Status: Arbeitsnotizen, nicht der finale Produktvertrag.

## Problemrahmen

Im heutigen CI-Schritt bleibt ein technisch erfolgreicher No-Mistakes-Lauf
aktiv, bis die PR gemergt, geschlossen oder abgebrochen wird. Das passt zu
menschengeführten TUI-Abläufen, blockiert aber einen Codex-Driver, der nach
geprüfter CI selbst den freigegebenen Merge ausführen soll.

## Gesicherte Fakten

- `outcome: checks-passed` ist bereits der agentengerechte, erfolgreiche
  Übergabepunkt: Der CI-Schritt bleibt im Hintergrund aktiv, der blockierende
  AXI-Aufruf endet aber bewusst für den Driver.
- Der Hintergrundmonitor beobachtet eine offene PR weiter, damit spätere
  Rebase- oder Konfliktfälle erkannt werden. Er ist kein Grund, dass Codex auf
  einen Merge oder Close warten muss.
- Die lokale Codex-Orchestrierung enthält bereits einen autorisierten
  Merge-Pfad. Der generierte No-Mistakes-Skill fordert dagegen noch den
  generischen menschlichen Handoff; diese widersprüchlichen Defaults sind die
  eigentliche Lücke.
- Die vorbereitete `axi watch`-Arbeit kann im Vordergrund bis
  `checks-passed`, Gate, Quiet oder Terminalzustand warten. Ein optionaler
  Hook-/Reentry-Supervisor ist davon technisch und sicherheitlich getrennt.

## Vorläufiges Zielbild

No-Mistakes hat bereits den agentengerechten Hand-off `outcome:
checks-passed`: Die CI ist grün, die PR ist bereit, und der AXI-Driver soll
nicht auf Merge oder Close warten. Der Hintergrundmonitor bleibt für spätere
Rebase-/Konfliktfälle bestehen.

Die Lücke ist damit nicht ein neuer Core-Outcome, sondern der Codex- und
Smart-Commit-Vertrag: Nach `checks-passed` muss ein bereits zum Merge
autorisierter Driver den normalen PR-/Merge-Pfad fortsetzen statt eine finale
Statusantwort zu senden oder auf manuelle Nachfrage zu warten.

## Bestätigte Entscheidungen

- D1: `checks-passed` bleibt der kanonische, rückwärtskompatible Handoff. Kein
  neuer persistenter `merge_ready`-Status und kein Alias in diesem Umfang.
- D2: Ein Merge folgt nur, wenn Smart Commit ihn bereits explizit freigegeben
  hat. Grüne CI allein erteilt keine Merge-Autorität.
- D3: Vor dem Merge prüft der Driver PR-Head, Mergeability und die
  No-Mistakes-Quittung erneut. Hat der Hintergrundmonitor nach
  `checks-passed` rebaset oder neu gepusht, wird nicht auf dem alten Receipt
  gemergt; der Driver wartet auf den aktuellen Hand-off oder folgt dem
  bestehenden Reconcile-Pfad.
- D4: Der erste Upstream-Versand enthält Vordergrund-`axi watch` und
  Codex-/Smart-Commit-Vertrag. Der Hook-/Reentry-Supervisor bleibt bewusst
  getrennt und nicht Teil dieses Versands.

## Elons Principles Guard

- Anforderung geprüft: Autonomer Abschluss eines bereits freigegebenen
  Smart-Commit-Merges, nicht ein zusätzlicher Pipeline-Status.
- Gelöscht: Neuer persistenter `merge_ready`-Status und verpflichtender Hook-
  Supervisor.
- Vereinfacht: Bestehendes `checks-passed` ist der einzige Übergabepunkt.
- Automation: Nur innerhalb einer explizit autorisierten Merge-Route;
  Stopp bei Gate, Head-Wechsel, unklarer Mergeability oder fehlender Quittung.
