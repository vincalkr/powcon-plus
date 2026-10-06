# powcon-plus

Quanto consuma la workstation? Il mio Corsair HX1200i lo dice, c'è il driver
`corsairpsu` nel kernel che lo legge via USB. Il problema è che `watch -n 1
sensors corsairpsu-hid-3-4` non è una cosa che tieni aperta: quindi questo
programma sta nell'icona di tray con potenza e kWh di oggi, somma i Wh della
giornata e li tiene su disco.

Nella barra di GNOME si vede tipo `148 W · 2.4 kWh` accanto all'icona.

## Requisiti

- Linux, modulo kernel `corsairpsu` (nei kernel recenti c'è) e il **cavo USB
  del PSU collegato**. Se `sensors` non elenca `corsairpsu-hid-3-4` qui non
  c'è niente da fare.
- Go 1.24 o superiore per compilare (io uso 1.26).
- Un tray che parli SNI: GNOME con l'estensione AppIndicator (su Ubuntu è già
  attiva), KDE, XFCE, Hyprland, swaybar.
- Per la finestra di dettaglio servono anche le librerie OpenGL/X11:
  `libgl1-mesa-dev libx11-dev libxcursor-dev libxrandr-dev libxinerama-dev
  libxkbcommon-dev`.

## Compilare e avviare

```bash
make all                       # bin/powcon-plus + bin/powcon-plus-gui
./bin/powcon-plus-gui run      # daemon + icona di tray
```

`make` da solo stampa l'elenco dei target.

Mentre provi, questi tornano comodi:

```bash
./bin/powcon-plus status           # lettura al volo, funziona anche senza daemon
./bin/powcon-plus report --days 7  # kWh e costo per giorno
./bin/powcon-plus window           # finestra di dettaglio
./bin/powcon-plus run --no-tray    # raccoglie e basta, senza icona
./bin/powcon-plus version          # versione
```

## L'icona di tray

Click sinistro apre la finestra. Click centrale o rotellina cambiano cosa
scrive il testo accanto all'icona (watt, energia di oggi, entrambi, solo
l'icona). Tasto destro apre il menu: finestra, report, etichetta, soglia
avviso, avvio automatico, esci.

Il testo usa la proprietà Ayatana `XAyatanaLabel`. Su GNOME/Ubuntu e KDE si
vede sempre, su altri compositori può capitare che compaia solo nel tooltip;
in quel caso metti `label_mode = "none"` e leggi il tooltip.

L'icona è disegnata dal programma (un fulmine, in tre colori a seconda che
stia raccogliendo, sia in pausa o abbia perso il sensore). Se preferisci
un'icona del tema imposta `icon_mode = "name"` e `icon_name = "powersupply"`,
però in quella modalità il colore non cambia più con lo stato.

## La finestra

In alto, da sinistra: il pulsante pausa/riprendi la raccolta, la select di
cosa mostra l'icona di tray, la spunta avvio automatico, la select lingua,
il campo costo/kWh con la moneta, Info (versione e sviluppatore), Report,
Esci. Sotto: lo stato del daemon, la potenza in grande, il grafico degli
ultimi 30 minuti, i valori di rail/12V/5V/3.3V/temperature/ventola e la
tabella dei giorni.

Il costo è una stima: la tariffa la inserisci tu nel campo costo/kWh, e la
moneta (EUR, USD, GBP, CNY, JPY) serve solo per il simbolo e per etichettare
il numero — non c'è nessun cambio. Il valore viene salvato in configurazione.

La pausa la comandi solo da qui: nel menu di tray non c'è, perché l'estensione
AppIndicator di GNOME non rinfresca i label del menu e la voce sarebbe sempre
vecchia. Quando sei in pausa l'icona cambia colore, quello sì.

## Avvio automatico

O la spunta dalla finestra (o dal menu di tray), o da CLI:

```bash
powcon-plus autostart-status
powcon-plus install-autostart      # scrive ~/.config/autostart/powcon-plus.desktop
powcon-plus uninstall-autostart
```

L'entry viene scritta con l'eseguibile che sta girando in quel momento, così
se cambi binario resta tutto coerente. Parte con la sessione grafica, che è
quello che serve al tray. Se preferisci systemd c'è `--mode systemd`: unit
utente agganciata a `graphical-session.target`, log su
`journalctl --user -u powcon-plus`. Un flock impedisce che partano due copie.

## Installazione

```bash
make install    # copia i due binari in ~/.local/bin
```

`powcon-plus` è il daemon senza cgo, piccolo e portabile. `powcon-plus-gui`
è lo stesso programma compilato con la finestra (tag `gui`): è quello da
mettere in avvio automatico, perché un solo binario fa sia daemon sia finestra.

Per togliere tutto:

```bash
powcon-plus uninstall          # daemon, autostart, binari
powcon-plus uninstall --purge  # e anche dati e configurazione
```

Senza `--purge` dati e configurazione restano, quindi se reinstalli ritrovi
lo storico.

## Configurazione

`~/.config/powcon-plus/config.toml`, da creare con `powcon-plus init-config`.
Le chiavi complete sono in [docs/config.example.toml](docs/config.example.toml);
quelle che si toccano di solito:

```toml
label_mode = "power"        # power | energy | both | none
poll_interval = "1s"        # sotto 200ms non ha senso, il PSU aggrega ~1s
price_per_kwh = 0.25        # la tua tariffa
currency = ""               # EUR | USD | GBP | CNY | JPY; vuoto = segue la lingua
alert_power_w = 800         # notifica se resti sopra questa soglia (0 = off)
alert_sustain = "15s"
sample_store = true         # false = non scrivere nessuno storico
sample_interval = "10s"
retention_days = 31         # giorni di campioni crudi da tenere
data_dir = ""               # vuoto = ~/.local/share/powcon-plus
sensor = ""                 # vuoto = auto-detect; es. "hwmon8" per forzare
language = ""               # it | en | de | fr | es | zh | ja; vuoto = dalla locale
```

`both` nell'etichetta dà `148 W · 2.41 kWh`; con `energy` sotto 1 kWh mostra i
Wh (`340 Wh`).

La lingua copre finestra, menu di tray e notifiche. Se il campo è vuoto viene
dalla locale del sistema (con fallback inglese) e si può cambiare al volo
dalla select nella finestra, che poi la salva.

Se `sample_store = false` non viene scritto nessun file di storico: restano
solo il lock e il socket. La tabella e il report mostrano quindi solo la
sessione corrente.

## I file

```
~/.local/share/powcon-plus/daily.json                 riepiloghi per giorno
~/.local/share/powcon-plus/samples-2026-09-30.jsonl   campioni grezzi, uno al giorno
~/.local/share/powcon-plus/control.sock               controllo del daemon
```

Per curiosarci dentro:

```bash
jq -s 'map(.w) | add/length' ~/.local/share/powcon-plus/samples-$(date +%F).jsonl
powcon-plus report --csv | column -t -s,
```

## Come funziona sotto

Legge `/sys/class/hwmon/hwmonN` dove `name = corsairpsu`, e risolve i canali
dalle **etichette** del driver, non dagli indici: gli indici cambiano tra le
versioni del driver, le label no.

Il PSU non ha un contatore di energia, quindi i Wh se li fa il programma
integrando la potenza col metodo del trapezio. Se il buco tra due letture è
più grande di `3 × poll_interval` quel tratto viene saltato, altrimenti una
sospensione del PC si trasformerebbe in kWh inventati.

Il daemon non usa cgo: tray e notifiche passano da D-Bus (`StatusNotifierItem`
+ `DBusMenu`), così il binario resta piccolo e si può cross-compilare. La
finestra invece è Fyne, e per quello serve il tag `gui`.

La versione sta in `internal/version`, unica fonte per `powcon-plus version` e
per il dialog Info.

## Se qualcosa non va

| Sintomo | Di solito è |
|---|---|
| `sensore corsairpsu non disponibile` | cavo USB scollegato, modulo non caricato, oppure OpenLinkHub/liquidctl/iCUE si è preso il device (in quel caso l'hwmon sparisce del tutto) |
| l'icona non c'è ma il daemon gira | estensione AppIndicator disattivata, o sessione senza bus grafico (ssh, service systemd globale) |
| il testo non si vede nella barra | il desktop non supporta `XAyatanaLabel`: `label_mode = "none"` e leggi il tooltip |
| `daemon non in esecuzione` da `ctl` | il socket è `~/.local/share/powcon-plus/control.sock`: guarda permessi e che il daemon ci sia davvero |
| consumi strani o lotte sul device | due copie del daemon: il flock dovrebbe evitarlo, controlla con `pgrep -af powcon-plus` |

## Test

```bash
make test     # unit test, con e senza tag gui
make vet fmt
```

I test che girano il giro completo SNI/DBusMenu si saltano da soli se non
c'è un bus di sessione disponibile.
