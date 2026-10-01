# powcon-plus

Potenza ed energia della workstation lette dall'alimentatore **Corsair HX1200i**
(driver `corsairpsu` del kernel), con icona di tray che mostra il consumo nella
barra, finestra di dettaglio, storico giornaliero e avvio automatico.

```
  ⚡ 148 W · 2.4 kWh          ← icona + testo nella barra di GNOME
```

Sostituisce `watch -n 1 sensors corsairpsu-hid-3-4` con qualcosa che resta in
vista, somma i Wh della giornata e li registra su disco.

## Requisiti

- Linux con il modulo kernel `corsairpsu` (incluso dai kernel recenti) e il
  **cavo USB del PSU collegato**; `sensors` deve mostrare `corsairpsu-hid-3-4`.
- Go ≥ 1.24 per la build (testato con 1.26).
- Desktop con system tray SNI: GNOME con estensione AppIndicator (Ubuntu ce
  l'ha già: `ubuntu-appindicators@ubuntu.com`), KDE, XFCE, Hyprland, swaybar.
- Solo per la finestra di dettaglio: OpenGL/X11 (`libgl1-mesa-dev`,
  `libx11-dev`, `libxcursor-dev`, `libxrandr-dev`, `libxinerama-dev`,
  `libxkbcommon-dev`).

## Build e avvio

```bash
make                 # bin/powcon-plus (senza cgo) + bin/powcon-plus-gui
./bin/powcon-plus-gui run          # daemon + icona di tray
```

Comandi utili mentre si prova:

```bash
./bin/powcon-plus status           # lettura istantanea (senza daemon funziona)
./bin/powcon-plus report --days 7  # kWh e costo per giorno
./bin/powcon-plus window           # finestra di dettaglio
./bin/powcon-plus run --no-tray    # solo raccolta, senza icona
```

## Avvio automatico

L'autostart si attiva **dall'interfaccia**: nella finestra di dettaglio c'è
la spunta **Avvio automatico**, e nel menu a destra dell'icona di tray la
voce **Avvio automatico → All'avvio della sessione**. Entrambi scrivono il
file XDG usando l'eseguibile attivo, quindi restano coerenti se cambi
binario.

```bash
powcon-plus autostart-status       # verifica lo stato
powcon-plus install-autostart      # (alternativa da CLI) scrive la desktop entry
powcon-plus uninstall-autostart
```

L'autostart XDG parte con la sessione grafica, che è ciò che serve
all'icona di tray. In alternativa `--mode systemd` usa un'unit utente
(`graphical-session.target`) con log in `journalctl --user -u powcon-plus`.
Un flock evita che partano due copie.

## Deploy (binario single-file)

```bash
make                 # bin/powcon-plus + bin/powcon-plus-gui
make install         # li copia in ~/.local/bin
```

`bin/powcon-plus` è il daemon senza cgo (piccolo, portabile).
`bin/powcon-plus-gui` include anche la finestra di dettaglio (Fyne) ed è
quello da mettere in autostart: con la spunta in UI o
`powcon-plus install-autostart` genera la desktop entry che punta a sé
stesso.

## Disinstallazione

```bash
powcon-plus uninstall          # ferma il daemon, toglie l'autostart e i binari
powcon-plus uninstall --purge  # in più cancella dati (~/.local/share/powcon-plus) e config
```

`uninstall` rimuove: il daemon in esecuzione, la desktop entry XDG,
l'eventuale unit systemd utente e i binari installati (più il fratello
`-gui`). Senza `--purge` dati e configurazione restano, così una
reinstallazione ritrova lo storico; con `--purge` torna come se non fosse
mai stato installato.

## Icona di tray

| Interazione | Effetto |
|---|---|
| click sinistro | apre la finestra di dettaglio |
| click centrale / rotellina | cambia cosa mostra il testo (W, energia di oggi, entrambi, solo icona) |
| tasto destro | menu: pausa, etichetta, soglia avviso, avvio automatico, salva, esci |

Il testo accanto all'icona usa la proprietà Ayatana `XAyatanaLabel`: su
GNOME/Ubuntu e KDE è sempre visibile, su altri compositori potrebbe comparire
solo nel tooltip. Se preferisci un'icona di tema invece di quella generata,
imposta `icon_mode = "name"` e `icon_name` nella configurazione.

## Configurazione

`~/.config/powcon-plus/config.toml` (da creare con `powcon-plus init-config`):

```toml
label_mode = "power"        # power | energy | both | none
poll_interval = "1s"        # minima 200ms: il PSU aggrega ~1s
price_per_kwh = 0.25        # tariffa per la stima del costo
currency = ""               # EUR | USD | GBP | CNY | JPY; vuoto = in base alla lingua
alert_power_w = 800         # notifica se la soglia è superata a lungo (0 = off)
alert_sustain = "15s"
sample_interval = "10s"     # ogni quanto salvare un campione
retention_days = 31         # giorni di campioni crudi da tenere
sensor = ""                 # vuoto = auto-detect; es. "hwmon8" per forzare
language = ""               # it | en | de | fr | es | zh | ja; vuoto = dalla locale
```

La lingua riguarda finestra, menu di tray e notifiche: se il campo è vuoto
viene rilevata dalla locale del sistema (fallback inglese), e può essere
cambiata in tempo reale dalla select **Lingua** nella finestra (la scelta
viene salvata nella configurazione).

Modalità etichetta: `both` mostra ad esempio `148 W · 2.41 kWh`,
`energy` sotto 1 kWh mostra i Wh (`340 Wh`).

## Dati

```
~/.local/share/powcon-plus/daily.json                 riepiloghi per giorno
~/.local/share/powcon-plus/samples-2026-09-30.jsonl   campioni grezzi (un file per giorno)
~/.local/share/powcon-plus/control.sock               controllo del daemon
```

Esempio di analisi con `jq`:

```bash
jq -s 'map(.w) | add/length' ~/.local/share/powcon-plus/samples-$(date +%F).jsonl
powcon-plus report --csv | column -t -s,
```

## Come funziona

- Lettura di `/sys/class/hwmon/hwmonN` (`name = corsairpsu`) risolvendo i
  canali **dalle etichette** del driver, non dagli indici.
- I Wh sono integrati dalla potenza (trapezio): il PSU non espone un
  contatore di energia. I buchi più grandi di `3 × poll_interval` non vengono
  conteggiati.
- Nessun cgo nel daemon: tray e notifiche passano da D-Bus
  (`StatusNotifierItem` + `DBusMenu`). Dettagli e motivazioni in
  [docs/DISEGNO.md](docs/DISEGNO.md).

## Risoluzione problemi

| Sintomo | Causa probabile |
|---|---|
| `sensore corsairpsu non disponibile` | cavo USB del PSU scollegato, modulo non caricato, oppure OpenLinkHub/liquidctl/iCUE ha acquisito il device (in quel caso l'hwmon sparisce) |
| icona assente ma il daemon gira | estensione AppIndicator disattivata, oppure sessione senza bus grafico (ssh, systemd service globale) |
| testo non visibile nella barra | il desktop non supporta `XAyatanaLabel`: usa `label_mode = "none"` e leggi il tooltip, o passa a KDE |
| `daemon non in esecuzione` da `ctl` | il socket è in `~/.local/share/powcon-plus/control.sock`: controlla permessi e che il daemon sia attivo |
| doppio consumo/duelli sul device | due copie del daemon: il flock lo impedisce, ma verifica con `pgrep -af powcon-plus` |

## Test

```bash
make test     # include il giro completo SNI/DBusMenu sul bus di sessione
make vet fmt
```

I test del tray si saltano da soli se non c'è un bus di sessione.
