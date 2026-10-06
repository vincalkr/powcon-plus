# powcon-plus

Monitoraggio di potenza ed energia della workstation a partire dai dati
rilevati dall'alimentatore **Corsair HX1200i**, esposti dal driver kernel
`corsairpsu` tramite hwmon.

Il programma resta residente: pubblica potenza e consumo giornaliero
accanto all'icona di tray, integra i Wh della giornata e li conserva su
disco. Una finestra di dettaglio offre grafico in tempo reale, tensioni,
temperature e riepilogo per giorno.

Nella barra di GNOME l'etichetta assume una forma come `148 W · 2.4 kWh`.

## Requisiti

- Linux con modulo kernel `corsairpsu` (presente dai kernel recenti) e cavo
  USB dell'alimentatore collegato. Il prerequisito si verifica con `sensors`:
  deve comparire il chip `corsairpsu-hid-3-4`.
- Go 1.24 o superiore per la compilazione (sviluppato e testato con 1.26).
- Un system tray che implementi il protocollo StatusNotifierItem: GNOME con
  estensione AppIndicator (già attiva su Ubuntu,
  `ubuntu-appindicators@ubuntu.com`), KDE, XFCE, Hyprland, swaybar.
- Per la sola finestra di dettaglio sono richieste le librerie OpenGL/X11:
  `libgl1-mesa-dev`, `libx11-dev`, `libxcursor-dev`, `libxrandr-dev`,
  `libxinerama-dev`, `libxkbcommon-dev`.

## Compilazione e avvio

```bash
make all                    # bin/powcon-plus + bin/powcon-plus-gui
./bin/powcon-plus-gui run   # daemon con icona di tray
```

Invocare `make` senza argomenti produce l'elenco dei target disponibili.

Comandi di utilità durante la prova:

```bash
./bin/powcon-plus status           # lettura immediata, funziona anche senza daemon
./bin/powcon-plus report --days 7  # kWh e costo per giorno
./bin/powcon-plus window           # finestra di dettaglio
./bin/powcon-plus run --no-tray    # sola raccolta dati, senza icona
./bin/powcon-plus version          # versione installata
```

## Icona di tray

| Interazione | Effetto |
|---|---|
| Click sinistro | apre la finestra di dettaglio |
| Click centrale o rotellina | alterna il contenuto dell'etichetta: potenza, energia del giorno, entrambi, sola icona |
| Click destro | menu: finestra, report, etichetta, soglia avviso, avvio automatico, esci |

L'etichetta accanto all'icona utilizza la proprietà Ayatana
`XAyatanaLabel`. Su GNOME/Ubuntu e KDE è sempre visibile; su altri
compositori può comparire soltanto nel tooltip. In quel caso conviene
impostare `label_mode = "none"` e leggere il tooltip.

L'icona è generata dal programma (un fulmine) e assume colore diverso a
seconda che la raccolta sia attiva, sospesa o che il sensore sia stato
perso. In alternativa è possibile usare un'icona del tema impostando
`icon_mode = "name"` e `icon_name` (ad esempio `powersupply`): in questa
modalità l'icona non cambia più colore con lo stato.

## Finestra di dettaglio

La barra superiore contiene, da sinistra: pulsante di pausa/ripresa della
raccolta, selettore del contenuto dell'etichetta di tray, spunta di avvio
automatico, selettore lingua, campo costo/kWh con relativo selettore di
moneta, pulsante Info (versione e sviluppatore), Report, Esci.

Sotto la barra sono presenti lo stato del daemon, la potenza di lettura,
il grafico degli ultimi 30 minuti, i valori di tensioni e temperature, la
ventola e la tabella dei giorni.

Il costo riportato è una stima: la tariffa viene inserita dall'utente nel
campo costo/kWh. Il selettore di moneta (EUR, USD, GBP, CNY, JPY) determina
solo il simbolo e l'etichetta del valore, senza alcuna conversione di
cambio. Il valore inserito viene persistito nella configurazione.

La pausa della raccolta è disponibile solo nella finestra. Nel menu di
tray è stata eliminata perché l'estensione AppIndicator di GNOME non
aggiorna le etichette del menu, che resterebbero incoerenti con lo stato.
Il cambio di stato resta visibile dal colore dell'icona.

## Avvio automatico

L'attivazione avviene dalla finestra di dettaglio o dalla voce nel menu di
tray, oppure da riga di comando:

```bash
powcon-plus autostart-status
powcon-plus install-autostart      # scrive ~/.config/autostart/powcon-plus.desktop
powcon-plus uninstall-autostart
```

La desktop entry viene scritta puntando all'eseguibile in uso al momento,
così resta coerente dopo un aggiornamento del binario. L'entry XDG parte
con la sessione grafica, condizione necessaria per l'icona di tray.

In alternativa è disponibile un'unit systemd utente con
`--mode systemd`, agganciata a `graphical-session.target`; i log si
leggono con `journalctl --user -u powcon-plus`. Un flock impedisce l'avvio
di più istanze.

## Installazione

```bash
make install    # copia i due binari in ~/.local/bin
```

`powcon-plus` è il daemon compilato senza cgo, di dimensioni ridotte e
portabile. `powcon-plus-gui` è lo stesso programma compilato con il tag
`gui`, che include anche la finestra: è il binario da destinare
all'avvio automatico, poiché un solo eseguibile svolge sia il ruolo di
daemon sia quello di finestra.

Disinstallazione:

```bash
powcon-plus uninstall          # arresta il daemon, rimuove autostart e binari
powcon-plus uninstall --purge  # rimuove anche dati e configurazione
```

Senza `--purge` dati e configurazione vengono conservati, quindi una
successiva installazione ritrova lo storico.

## Configurazione

Il file è `~/.config/powcon-plus/config.toml`, generabile con
`powcon-plus init-config`. L'elenco completo delle chiavi con descrizione
si trova in [docs/config.example.toml](docs/config.example.toml). Le più
rilevanti:

```toml
label_mode = "power"        # power | energy | both | none
poll_interval = "1s"        # sotto i 200ms non ha senso: il PSU aggrega ~1s
price_per_kwh = 0.25        # tariffa per la stima del costo
currency = ""               # EUR | USD | GBP | CNY | JPY; vuoto = segue la lingua
alert_power_w = 800         # notifica al superamento prolungato della soglia (0 = disattivo)
alert_sustain = "15s"
sample_store = true         # false = nessuna scrittura dello storico
sample_interval = "10s"
retention_days = 31         # giorni di campioni grezzi da conservare
data_dir = ""               # vuoto = ~/.local/share/powcon-plus
sensor = ""                 # vuoto = auto-rilevamento; es. "hwmon8" per forzare
language = ""               # it | en | de | fr | es | zh | ja; vuoto = dalla locale
```

Con `label_mode = "both"` l'etichetta assume una forma come
`148 W · 2.41 kWh`; con `energy`, sotto 1 kWh, vengono mostrati i Wh
(`340 Wh`).

La lingua si applica a finestra, menu di tray e notifiche. Se il campo è
vuoto viene determinata dalla locale di sistema, con fallback inglese. Può
essere modificata in tempo reale dal selettore nella finestra; la scelta
viene salvata in configurazione.

Con `sample_store = false` non viene scritto alcun file di storico: nella
cartella restano solo il lock e il socket. Tabella e report mostrano
quindi esclusivamente i dati della sessione corrente.

## File prodotti

```
~/.local/share/powcon-plus/daily.json                 riepiloghi per giorno
~/.local/share/powcon-plus/samples-2026-09-30.jsonl   campioni grezzi, un file per giorno
~/.local/share/powcon-plus/control.sock               socket di controllo del daemon
```

Esempi di analisi:

```bash
jq -s 'map(.w) | add/length' ~/.local/share/powcon-plus/samples-$(date +%F).jsonl
powcon-plus report --csv | column -t -s,
```

## Architettura

La lettura avviene da `/sys/class/hwmon/hwmonN` con `name = corsairpsu`.
I canali sono risolti a partire dalle **etichette** esposte dal driver e
non dagli indici, che variano tra le versioni del driver stesso.

L'alimentatore non espone un contatore di energia: i Wh sono integrati
dalla potenza con metodo del trapezio. Gli intervalli tra due letture
superiori a `3 × poll_interval` vengono esclusi dall'integrazione, per non
attribuire consumo a periodi in cui il PC era sospeso o il daemon fermo.

Il daemon non utilizza cgo: tray e notifiche passano attraverso D-Bus
(`StatusNotifierItem` e `DBusMenu`), così il binario resta di piccole
dimensioni e cross-compilabile. La finestra è realizzata con Fyne e
richiede il tag di compilazione `gui`.

La versione è definita in `internal/version`, unica origine per il comando
`powcon-plus version` e per il dialog Info della finestra.

## Risoluzione dei problemi

| Sintomo | Causa più probabile |
|---|---|
| `sensore corsairpsu non disponibile` | cavo USB scollegato, modulo non caricato, oppure OpenLinkHub/liquidctl/iCUE ha acquisito il device (in tal caso il nodo hwmon scompare) |
| Icona assente pur con daemon in esecuzione | estensione AppIndicator disattivata, oppure sessione priva di bus grafico (connessione ssh, service systemd globale) |
| Etichetta non visibile nella barra | desktop senza supporto `XAyatanaLabel`: impostare `label_mode = "none"` e fare riferimento al tooltip |
| `daemon non in esecuzione` dai comandi `ctl` | verificare che il daemon sia attivo e i permessi su `~/.local/share/powcon-plus/control.sock` |
| Letture incoerenti o accesso concorrente al device | più istanze del daemon: il flock dovrebbe escluderle, verificare con `pgrep -af powcon-plus` |

## Test

```bash
make test     # test unitari, con e senza tag gui
make vet fmt
```

I test che esercitano il giro completo SNI/DBusMenu vengono saltati
automaticamente in assenza di un bus di sessione.
