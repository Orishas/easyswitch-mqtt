# easyswitch-mqtt: Design

Stand: 2026-11-12. Arbeitsname des Repos: `easyswitch-mqtt` (öffentlich, MIT).

## Absicht

Logitech-Maus und -Tastatur mit Easy-Switch sollen aus Home Assistant auf Kanal 1, 2 oder 3 geschaltet werden
können. Anlass: Am Schreibtisch wechselt Home Assistant die Quelle des Monitors (PC, Mac, Konsole); Maus und
Tastatur sollen dabei mitgehen, ohne dass jemand die Tasten an den Geräten drückt.

Das Projekt wird veröffentlicht. Es darf nichts von einem bestimmten Schreibtisch voraussetzen: keine festen
Rechnernamen, keine feste Zahl von Rechnern, keine feste Kanalbelegung.

## Was gemessen wurde (2026-11-12, MX Master 4 + MX Keys S, Logi Bolt, Windows 11 und macOS)

Getestet mit `marcocosta97/mxswitch` (MIT), bei laufendem Logi Options+ mit aktivem „Enhanced Easy-Switch“:

- Beide Geräte sitzen hinter dem Bolt-Empfänger (USB `046d:c548`), Tastatur auf Platz 1, Maus auf Platz 2.
  Beide melden 3 Kanäle und denselben aktuellen Kanal (PC = 1, Mac = 2).
- Der Kanalwechsel per HID++ (Feature `0x1814`, Funktion 1) funktioniert für beide Geräte, unter Windows und
  unter macOS, neben Options+.
- **Die Erkennung von mxswitch ist unzuverlässig.** In etwa der Hälfte der Läufe wurde kein Gerät oder nur
  eines gefunden. Gefunden wird ein Gerät praktisch nur, wenn es kurz vorher bewegt oder getippt wurde. Ob die
  Geräte nur langsamer antworten, als mxswitch wartet (0,3 bis 0,4 s), oder gar nicht erreichbar sind, ist
  nicht gemessen.
- Options+ zieht die Maus der Tastatur nach, wenn die Easy-Switch-Taste der Tastatur gedrückt wird. Umgekehrt
  nicht. Ob es auch bei einem Wechsel per Befehl nachzieht, ist unbekannt und wird nicht vorausgesetzt.
- macOS hat im Test (angemeldeter Benutzer, Terminal) nicht nach der Freigabe „Eingabeüberwachung“ gefragt.
  Das beweist noch nichts für den Dienst: Ein Terminal, das die Freigabe schon hat, gibt sie an gestartete
  Programme weiter. Ob ein Systemdienst vor der Anmeldung zugreifen darf, klärt `diag`.

## Ziele

1. Aus Home Assistant „schalte auf Kanal N“ auslösen, N = 1 bis 3, für alle Easy-Switch-Geräte am Rechner
   gemeinsam.
2. Kanäle tragen Namen, die in Home Assistant gepflegt und angezeigt werden.
3. Home Assistant zeigt wahrheitsgemäß, wo die Geräte sind und was mit dem letzten Auftrag passiert ist.
4. Ein Rechner, der aus ist oder schläft, führt zu einer klaren Meldung, nicht zu einem hängenden oder
   falschen Zustand.
5. Der Dienst läuft ab dem Systemstart, also schon am Anmeldebildschirm.
6. Einrichtung in wenigen Minuten ohne Handarbeit an Konfigurationsdateien: ein Befehl, wenige Fragen.
7. Schlank: ein Programm je Betriebssystem, keine mitgelieferte Laufzeit, kein Fenster, kein Tray.

## Nicht-Ziele der ersten Version

- Geräte, die direkt per Bluetooth verbunden sind; Unifying-Empfänger. (Der Aufbau soll beides später
  zulassen: die Geräteadresse `0xFF` und andere Empfänger-Kennungen sind nur weitere Fälle.)
- Linux als Rechner mit Dienst. (Der Protokollteil wird trotzdem unter Linux gebaut und getestet.)
- Eine eigene Home-Assistant-Integration. MQTT Discovery reicht.
- Akkustand, Tastenbelegung, DPI und alles andere, was Options+ macht.
- Die Gegenrichtung „Easy-Switch-Taste gedrückt, Monitor schaltet mit“. Der Dienst meldet den Kanal, eine
  Automation könnte darauf reagieren; gebaut und dokumentiert wird das hier nicht.
- Änderungen am Tessera-Plugin und am Home-Assistant-Skript des Auftraggebers. Die kommen danach.

## Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)

- „Erfolg“ melden, wenn nur ein Befehl abgeschickt wurde und niemand weiß, ob das Gerät angekommen ist.
- Ein Gerät stillschweigend auf einen Kanal schicken, an dem kein Rechner gekoppelt ist.
- Einen alten Auftrag nach einem Neustart des Dienstes oder des Brokers nachholen.
- Options+ stören (Geräte exklusiv öffnen, fremde Antworten verschlucken).
- Pro Rechner eintragen müssen, welcher Kanal er ist.

## Wege, die erwogen wurden

| Weg | Abwägung |
|---|---|
| **Eigener Dienst auf jedem Rechner, MQTT Discovery** (gewählt) | Gleiches Programm überall, Rückmeldung, ein Gerät in Home Assistant. Mehr Bau. |
| mxswitch von außen anstoßen (HASS.Agent unter Windows, SSH am Mac) | Fast kein Code, aber zwei Mechanismen, keine Rückmeldung, und die gemessene Unzuverlässigkeit bleibt. |
| Mouser erweitern | Nur sinnvoll als Ersatz für Options+; Options+ soll aber bleiben. |
| Bolt-Funk auf einem ESP32 nachbauen | Würde das Problem „Rechner ist aus“ lösen; Kopplung und Verschlüsselung sind nicht offen, Erfolg ungewiss. |

Gewählt vom Auftraggeber: der eigene Dienst.

## Entscheidungen des Auftraggebers

- Universell: Kanal 1, 2 oder 3, nicht „PC/Mac“.
- Kanalnamen setzbar und in Home Assistant sichtbar.
- Sauberes Fehlerverhalten in Home Assistant, weil Rechner aus sein können.
- Dienst, der am Anmeldebildschirm schon läuft.
- Einfaches, narrensicheres Setup; schlanker Dienst.
- Neues, öffentliches Repo.

## Entscheidungen des Entwurfs (meine, korrigierbar)

### Sprache und Form

Go. Ein statisches Programm `easyswitch` je Plattform (Windows amd64/arm64, macOS universal). HID unter Windows
über `setupapi`/`hid.dll`, unter macOS über IOKit (cgo, gebaut auf macOS-Runnern). Kein Python, kein
Paketieren einer Laufzeit.

Befehle:

```
easyswitch setup       fragt Broker-Adresse, Port, Benutzer, Passwort; prüft Verbindung und Geräte;
                       richtet den Dienst ein und startet ihn
easyswitch run         der Dienst selbst (vom System gestartet)
easyswitch status      was der laufende Dienst sieht: Geräte, Kanäle, Broker, letzter Auftrag
easyswitch switch N    lokal umschalten, ohne MQTT (Test und Notfall)
easyswitch diag        Messprogramm, siehe „Erster Meilenstein“
easyswitch uninstall   Dienst entfernen, eigene Einträge am Broker löschen
```

### Geräte beobachten statt bei jedem Auftrag suchen

Der Dienst hält die HID++-Schnittstelle des Empfängers dauerhaft offen (nicht exklusiv) und führt eine Tabelle:
je Platz am Empfänger Name, Stelle des Features `0x1814`, Zahl der Kanäle, aktueller Kanal, erreichbar ja/nein,
zuletzt gesehen. Die Tabelle entsteht aus

- den Meldungen des Empfängers, wenn ein Gerät kommt oder geht,
- einer geduldigen Abfrage beim Start und danach in größeren Abständen,
- dem, was beim letzten Mal gelernt wurde (Name und Feature-Stelle werden gemerkt und beim Benutzen geprüft).

Wartezeiten und Wiederholungen werden aus den Messwerten des Diagnoseprogramms festgelegt, nicht geraten.

### Ein Auftrag und was aus ihm wird

Ein Auftrag ist „Ziel = Kanal N“. Der Dienst auf dem Rechner, an dem Geräte hängen, führt ihn aus:

1. Für jedes Gerät, das nicht schon auf N ist: prüfen, ob auf Kanal N ein Rechner gekoppelt ist (sofern das
   Gerät das meldet, HID++ `0x1815`); wenn nicht, ablehnen.
2. Erreichbare Geräte sofort umschalten. Ein Gerät gilt als umgeschaltet, wenn es danach vom Empfänger
   verschwindet; antwortet es weiter, wird wiederholt.
3. Nicht erreichbare Geräte **vormerken**: Sobald das Gerät aufwacht (erste Bewegung, erster Tastendruck),
   wird es umgeschaltet. Die Vormerkung endet durch den nächsten Auftrag, nach einer Frist (Vorschlag 10
   Minuten, einstellbar) oder mit dem Ende des Dienstes. Sie wird nirgends gespeichert.

Zustände des Auftrags, wie Home Assistant sie sieht:

| Status | Bedeutung |
|---|---|
| `bereit` | kein Auftrag offen |
| `umgeschaltet` | alle Geräte haben diesen Rechner Richtung Kanal N verlassen |
| `angekommen` | der Dienst am Zielrechner sieht die Geräte (nur wenn dort einer läuft) |
| `wartet auf <Gerät>` | vorgemerkt, Gerät schläft |
| `wartet auf <Rechner>` | vorgemerkt, Zielrechner noch nicht da |
| `Fehler: …` | mit Grund, siehe unten |

Regeln dazu (meine Entscheidungen):

- **Meldet ein Gerät nicht, welche Kanäle gekoppelt sind,** wird trotzdem umgeschaltet, und der Status trägt
  den Zusatz „Zielkanal ungeprüft“. Abgelehnt wird nur, was sicher ins Leere ginge.
- **Kann der Empfänger „schläft hier“ nicht von „ist auf einem anderen Kanal“ unterscheiden,** zeigt der
  Gerätesensor nach einer Frist `unbekannt` statt eines geratenen Kanals. Der zuletzt bekannte Kanal steht im
  Attribut.
- **Der Status bleibt stehen, bis der nächste Auftrag kommt.** Nach einem Neustart des Dienstes steht er auf
  `bereit`; der Dienst räumt dabei seinen eigenen alten Stand am Broker weg.
- **Hängen Maus und Tastatur an verschiedenen Rechnern,** führen beide Dienste denselben Auftrag aus. Jeder
  meldet das Ergebnis je Gerät; der gemeinsame Status ergibt sich daraus nach fester Rangfolge: Fehler vor
  „wartet“ vor „umgeschaltet“ vor „angekommen“.
- **Zielrechner aus oder ohne Dienst:** siehe „Zielrechner aus oder im Hochfahren“.

Fehlergründe, jeweils als lesbarer Text und als fester Code im Attribut:

- `no_holder`: Kein laufender Rechner hat die Geräte. Der Text nennt, wo sie zuletzt gesehen wurden.
- `channel_unpaired`: Auf dem Zielkanal ist kein Rechner gekoppelt.
- `timeout`: Vormerkung für ein schlafendes Gerät abgelaufen.
- `target_offline`: Der Rechner hinter dem Zielkanal ist nicht erreichbar.
- `device_error`: Gerät hat den Befehl mit einem Fehler beantwortet oder verlässt den Rechner nicht.
- `no_receiver`: Auf diesem Rechner ist kein Empfänger erreichbar (auch: Zugriff verweigert).

### In Home Assistant: ein Gerät, egal wie viele Rechner

```
Easy-Switch
  Ziel              Auswahl   PC / Mac / Kanal 3        (Optionen = die Kanalnamen)
  Kanal 1 / 2 / 3   Tasten    „Auf Kanal N schalten“    (unabhängig von den Namen)
  Status            Sensor    bereit / umgeschaltet / wartet auf … / Fehler: …
  <Gerätename>      Sensor    je Gerät: Kanal, Attribute: erreichbar, zuletzt gesehen, an welchem Rechner
  Name Kanal 1-3    Textfeld  Diagnose-Bereich
  <Rechnername>     Verbindungssensor je Rechner mit Dienst (Diagnose)
```

- „Ziel“ zeigt den Kanal, auf dem die Geräte zuletzt gemeinsam gesehen oder hingeschickt wurden. Sind Maus und
  Tastatur auf verschiedenen Kanälen, zeigen das die Gerätesensoren und der Status.
- Die Namen liegen als aufbewahrte MQTT-Nachrichten am Broker. Vorgabe ist „Kanal N“. Kein Rechner muss sie
  kennen oder gleich konfiguriert sein.
- Das Gerät ist „nicht verfügbar“, wenn kein einziger Dienst verbunden ist. Dafür meldet sich jeder Dienst mit
  einem eigenen Lebenszeichen (Last Will) an, und die gemeinsamen Einträge nennen alle bekannten Dienste mit
  „verfügbar, wenn einer da ist“.
- Aufträge werden nicht aufbewahrt (QoS 1, kein Retain). Ein Dienst, der gerade startet, sieht keine alten
  Aufträge.

### Mehrere Dienste, ein gemeinsames Gerät

Alle Dienste benutzen denselben Themenbaum (Vorgabe `easyswitch/`, im Setup änderbar für mehrere
Schreibtische) und veröffentlichen dieselben Discovery-Einträge mit denselben Kennungen; der Inhalt ist
deterministisch, sodass es gleich ist, wer zuletzt geschrieben hat. Jeder Dienst trägt sich unter
`easyswitch/agents/<id>` ein; aus der Liste entsteht die Verfügbarkeit.

Wer führt aus: der Dienst, der Geräte am eigenen Empfänger sieht. Er bestätigt die Übernahme auf einem
gemeinsamen Thema. Sieht ein Dienst keine Geräte und kommt binnen kurzer Frist keine Übernahme von einem
anderen, meldet er `no_holder`. Mehrere Dienste ohne Geräte melden dann dasselbe; das ist gewollt und
unschädlich.

Die genaue Themenstruktur und die Nutzlasten sind Sache des Bauenden, solange sie in der Anleitung
beschrieben sind und sich zwischen Versionen nicht stillschweigend ändern.

### Einrichtung

- **Windows:** eine Zeile in PowerShell (als Administrator), die das Programm aus dem GitHub-Release nach
  `%ProgramFiles%\easyswitch` lädt und `easyswitch setup` startet. Dienst als Windows-Dienst, Start
  automatisch, Neustart bei Absturz.
- **macOS:** eine Zeile im Terminal, die das Programm nach `/usr/local/bin` lädt und `sudo easyswitch setup`
  startet. Dienst als LaunchDaemon (läuft vor der Anmeldung).
- Setup fragt nur: Broker-Adresse (mit Suche nach `homeassistant.local` als Vorschlag), Port, Benutzer,
  Passwort, optional TLS und Themenbaum. Es prüft die Verbindung und zeigt die gefundenen Geräte, bevor es den
  Dienst einrichtet. Schlägt etwas fehl, sagt es was und richtet nichts halb ein.
- Konfiguration in einer Datei, die nur das System und Administratoren lesen dürfen
  (`%ProgramData%\easyswitch\config.json`, `/Library/Application Support/easyswitch/config.json`).
- Programme sind zunächst nicht signiert. Die Einzeiler laden per Skript, wodurch die Warnungen für
  heruntergeladene Programme nicht greifen sollen; das ist zu prüfen und in der Anleitung ehrlich zu
  beschreiben. Signieren ist eine spätere Entscheidung des Auftraggebers (kostet Geld).
- Update: derselbe Einzeiler noch einmal; die Konfiguration bleibt.

### Erster Meilenstein: messen, bevor der Dienst entsteht

`easyswitch diag` ist das Erste, was gebaut und auf beiden Rechnern des Auftraggebers ausgeführt wird. Es
beantwortet, mit Zahlen:

1. Wie lange brauchen Maus und Tastatur für eine Antwort: direkt nach Benutzung, nach 10 s, 1 min, 5 min,
   30 min Ruhe? Antworten sie spät oder gar nicht, und was meldet der Empfänger dann?
2. Welche Meldungen schickt der Empfänger, wenn ein Gerät aufwacht, einschläft, den Kanal wechselt?
3. Melden die Geräte, welche Kanäle gekoppelt sind (`0x1815`)?
4. Geht alles auch als Systemdienst vor der Anmeldung: Windows-Dienst in Sitzung 0, macOS-LaunchDaemon als
   root? Verlangt macOS dabei die Freigabe „Eingabeüberwachung“, und lässt sie sich einem Systemdienst geben?
5. Stört der dauerhaft geöffnete Zugriff Options+ oder umgekehrt?

Ergebnis ist ein Textbericht, den der Auftraggeber zurückschickt. Scheitert Punkt 4 oder 5 auf einem der
beiden Systeme, wird angehalten und mit dem Auftraggeber geklärt, wie es weitergeht; es wird nicht von selbst
auf einen schwächeren Aufbau ausgewichen. Je nach Ergebnis:

- Punkt 4 unter macOS scheitert: Der Dienst läuft dort als Benutzer-Agent nach der Anmeldung statt vor ihr,
  und die Anleitung sagt das. Das ist dann mit dem Auftraggeber zu klären, bevor weitergebaut wird.
- Geräte antworten nur spät, aber verlässlich: Vormerken wird selten gebraucht, bleibt aber.
- Geräte sind in Ruhe unerreichbar: Vormerken ist der Normalfall, und die Anleitung sagt deutlich „nach dem
  Umschalten Maus und Tastatur einmal anfassen“.

### Prüfen

- Protokollteil mit den Beispiel-Frames aus mxswitch (MIT, mit Hinweis übernommen) und eigenen aus `diag`.
- Ein nachgebauter Empfänger im Test (Geräte kommen, gehen, schlafen, antworten spät, antworten mit Fehler),
  gegen den Beobachtung, Auftrag, Vormerkung und alle Fehlergründe laufen.
- MQTT-Seite gegen einen Broker im Test: Discovery-Inhalt, Verfügbarkeit mit zwei Diensten, kein Nachholen
  alter Aufträge, `no_holder`.
- CI baut und testet unter Windows, macOS und Linux; Release-Workflow legt die Programme und Einzeiler ab.
- Am echten Gerät prüft der Auftraggeber: beide Richtungen, schlafendes Gerät, Rechner aus, Dienst am
  Anmeldebildschirm, Options+ läuft weiter.

## Zielrechner aus oder im Hochfahren (vom Auftraggeber entschieden)

Ein Dienst lernt seinen eigenen Kanal, sobald er Geräte sieht, und veröffentlicht ihn. Damit ist bekannt, ob
hinter einem Zielkanal ein Rechner mit Dienst steht und ob dieser gerade läuft.

Der Auftraggeber: Stur ablehnen wäre falsch, denn beim Quellenwechsel wird der Zielrechner oft gerade erst
eingeschaltet. Ablehnen soll eine Option sein.

Daraus (Verhalten als Einstellung in Home Assistant, „Wenn der Zielrechner aus ist“):

- **Warten** (Vorgabe, meine Wahl): Der Auftrag wird vorgemerkt, Status `wartet auf <Rechnername>`. Sobald der
  Dienst des Zielrechners sich meldet (er startet mit dem System, also schon am Anmeldebildschirm), wird
  umgeschaltet. Nach einer Frist (Vorschlag 3 Minuten, einstellbar) endet der Auftrag mit `target_offline`,
  die Geräte bleiben.
- **Sofort schalten:** ohne Rücksicht auf den Zielrechner, Status mit Warnung.
- **Ablehnen:** sofort `target_offline`.

Kanäle, hinter denen nie ein Dienst gesehen wurde (Konsole, Tablet), werden immer sofort geschaltet. Die
Vormerkung folgt denselben Regeln wie die für schlafende Geräte: ersetzt durch den nächsten Auftrag, nie
gespeichert, nie nach einem Neustart nachgeholt.

### Der Rechner, an dem die Geräte hängen, fährt herunter oder geht schlafen

Anmerkung des Auftraggebers: Fährt eine Automation den PC herunter, muss der Wechsel (etwa zum Mac) sofort
gehen. Daraus:

- Der Dienst bekommt vom System mit, dass der Rechner herunterfährt oder schlafen geht. Ein vorgemerkter
  Auftrag wird in diesem Moment **sofort ausgeführt**, statt zu verfallen, gleich welche Einstellung für den
  Zielrechner gilt: Wer jetzt nicht schaltet, kann es nicht mehr.
- Je Rechner einstellbar: „Beim Herunterfahren und Schlafen Geräte abgeben an: nichts / Kanal N“. Vorgabe
  „nichts“. Damit gehen Maus und Tastatur von selbst weg, auch wenn niemand einen Auftrag gegeben hat.
- Grenze, die in der Anleitung stehen muss: Auch hier lässt sich nur ein waches Gerät schalten. Eine
  Automation sollte deshalb **erst** das Ziel setzen und **dann** den Rechner herunterfahren.
- Ob der Dienst beim Herunterfahren genug Zeit bekommt und die Geräte dann noch antworten, ist zu messen
  (Plan 2, am echten Rechner).

Freigegeben vom Auftraggeber am 2026-11-12.

## Was dem Bauenden überlassen ist

Paketaufteilung, MQTT-Bibliothek, Themenstruktur im Einzelnen, Format der gemerkten Gerätedaten, genaue
Abstände und Wartezeiten (aus den Messwerten), Wortlaut der Meldungen (Deutsch und Englisch über
Übersetzungsschlüssel von Home Assistant, wo Discovery das erlaubt; sonst Englisch), Aufbau der Anleitung.
Repo-Sprache ist Englisch (README, Code, Commits), wie in den anderen Repos des Auftraggebers.
