# powcon-plus — progetto

Monitor di potenza ed energia di una workstation che usa un alimentatore
**Corsair HX1200i (2025)** come *sensore di misura*, con icona di system tray
(potenza/energia nella barra), finestra di dettaglio, storico su disco e
avvio automatico nella sessione utente. Scritto in Go, senza cgo nella parte
daemon.

Stato: prototipo funzionante (v0.1) verificato su Ubuntu GNOME/Wayland,
kernel 7.0.12, `go 1.26`.

---

## 1. Perché il PSU come punto di misura

| Opzione | Cosa misura | Pro | Contro |
|---|---|---|---|
| **hwmon `corsairpsu`** (scelta) | potenza **AC assorbita da rete** | misura vera a muro (include perdite di conversione, periferiche, monitor se collegato allo stesso alimentatore), zero root, zero demoni proprietari | serve il cavo USB del PSU; il chip è uno solo, non si attribuisce il consumo ai singoli componenti |
| `sensors -j` / lm-sensors | idem | già usato oggi con `watch` | processo figlio a ogni campione, dipendenza da lm-sensors, nessuna aggregazione |
| `liquidctl` / OpenLinkHub / iCUE | idem + RGB/pompe | più dispositivi Corsair | acquisiscono il device HID e **tolgono** l'hwmon del kernel: incompatibile con la misura continua |
| IPKVM / presa intelligente esterna | consumo a muro reale | indipendente dal PC | costo, niente correlazione con i processi |
| telemetria CPU/GPU (`k10temp`, `amdgpu`) | potenza dei singoli chip | utile per attribuzioni | non è il consumo totale; `k10temp` non espone potenza su tutte le piattaforme |

Dati osservati sulla macchina reale (`/sys/class/hwmon/hwmon8`, `name=corsairpsu`):
`power1_input` in **microwatt** (`148000000` → 148 W), `in*` in millivolt,
`curr*` in milliampere, `temp*` in milligradi. 20 letture sysfs in 55 ms: il
costo di acquisizione è trascurabile, mentre l'aggregazione interna del PSU è
di ~1 s → **poll predefinito 1 s** (minimo 200 ms).

Il driver non espone un contatore di energia: i **Wh sono ottenuti per
integrazione** della potenza nel tempo (metodo del trapezio), con i buchi più
grandi di `3 × poll_interval` esclusi (PC spento, daemon in pausa, sensore
assente).

## 2. Architettura

```
        ┌──────────────────────── powcon-plus run (daemon, senza cgo) ─────────────────────┐
        │                                                                                  │
 sysfs  │  internal/psu        internal/energy        internal/store                       │
hwmon8 ─┼─► lettura canali ──► integrazione Wh ──────► samples-YYYY-MM-DD.jsonl             │
        │  (etichette→canali)  (oggi/sessione/rail)    daily.json (rollup atomico)          │
        │        │                    │                        │                           │
        │        ▼                    ▼                        ▼                           │
        │  internal/status.Snapshot ───────────────────────────┴─── internal/ctl (socket    │
        │        │                                                   Unix JSON line)       │
        │        ▼                                                                    │      │
        │  internal/tray  SNI + DBusMenu (godbus)                                      │      │
        │   • icona (pixmap ARGB32)                                                    │      │
        │   • XAyatanaLabel → testo in barra GNOME                                     │      │
        │   • menu: pausa, etichetta, soglia, autostart, esci                          │      │
        └──────────────────────────────────────────────────────────────────────────────┘────┘
                                        │  subscribe (stream 1 Hz)
                                        ▼
                   powcon-plus window  (internal/gui, Fyne, tag `gui`)
                   grafico 30 min · rail · tabella 14 giorni · comandi
```

Processi: **un solo daemon** in sessione grafica. La finestra è un client
leggero (stesso formato `Snapshot`), così il daemon resta piccolo, senza
OpenGL, e può girare anche su una macchina senza GUI (`--no-tray`, ssh).

## 3. Icona di tray: come funziona davvero

L'estensione Ubuntu `ubuntu-appindicators@ubuntu.com` (e le simili
KStatusNotifierItem) è stata letta nei sorgenti per adattare il protocollo:

- registrazione: `org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem(nome)`
  con un **bus name** (formato KDE `org.kde.StatusNotifierItem-<pid>-1`);
  l'oggetto è esposto su `/StatusNotifierItem`;
- proprietà obbligatorie lette con `org.freedesktop.DBus.Properties.GetAll`
  (`Category`, `Id`, `Title`, `Status`, `WindowId`, `IconThemePath` come
  `s`, `Menu`, `ItemIsMenu`, `IconName`, `IconPixmap`, overlay/attention);
- **testo in barra**: proprietà **`XAyatanaLabel`** (+`XAyatanaLabelGuide`),
  che l'estensione legge e mostra accanto all'icona; `IconAccessibleDesc` per
  gli screen reader. Su KDE il testo viene da `Title`/label del pannello;
- aggiornamenti: segnale `org.freedesktop.DBus.Properties.PropertiesChanged`
  (l'estensione ri-legge `XAyatanaLabel` e ridisegna l'icona);
- menu: `com.canonical.dbusmenu` su `/MenuBar`, con `GetLayout(i,i,as) →
  (u,(ia{sv}av))`, `GetGroupProperties(ai,as)` (l'estensione passa **ids** come
  primo argomento e lista vuota = tutte le proprietà), `Event(i,s,v,u)`,
  `AboutToShow(i)`, segnale `LayoutUpdated(u,i)`; proprietà voce: `label`,
  `enabled`, `visible`, `type`, `children-display=submenu`, `toggle-type`,
  `toggle-state`;
- icona: `IconPixmap` = `a(iiay)`, dati **ARGB32 network byte order**; il glifo
  è generato a runtime (poligono "fulmine", supersampling 4× per
  l'antialiasing) a 22 e 44 px, quindi niente file di risorse.

Librerie valutate e scartate: `fyne.io/systray` (non espone `XAyatanaLabel`,
quindi in GNOME resterebbe solo l'icona), `getlantern/systray` (cgo +
libappindicator), `cajones/go-sni`/`gsni` (non recuperabili dal proxy moduli).
L'implementazione propria in `internal/tray` sono ~600 righe e permette di
pubblicare etichetta, icona dinamica e menu con controllo totale.

Interazioni previste sul simbolo: click sinistro = finestra di dettaglio;
click centrale/rotellina = cambia modalità etichetta; tasto destro = menu.

## 4. Dati e persistenza

```
~/.config/powcon-plus/config.toml      configurazione (TOML, v1.6 BurntSushi)
~/.local/share/powcon-plus/
    daily.json                         rollup giornalieri, scrittura atomica
    samples-2026-09-30.jsonl           campioni (default 1 ogni 10 s, ~1 MB/giorno)
    lock                               flock single-instance
    control.sock                       socket di controllo (0600)
```

`daily.json` (per giorno, fuso locale): `wh` (AC), `wh12/wh5/wh33` (stima sui
rail), `samples`, `max_w`, `min_w`, `duration_s`, `updated`. `AvgW` è derivato
(`Wh × 3600 / duration_s`), il costo da `price_per_kwh` (in `currency`).
Retention dei campioni crudi: `retention_days` (default 31); i rollup non
vengono potati. `sample_store = false` disattiva la scrittura di entrambi i
file di storico (tabella e report limitati alla sessione corrente).

## 5. Configurazione

```toml
sensor = ""                  # vuoto = auto-detect hwmon name "corsairpsu"
poll_interval = "1s"
label_mode = "power"         # power | energy | both | none
format_w  = "%.0f W"
format_wh = "%.2f kWh"
price_per_kwh = 0.25
currency = ""                # EUR | USD | GBP | CNY | JPY (vuoto = dalla lingua)
alert_power_w = 0            # 0 = disattivo
alert_sustain = "15s"
alert_cooldown = "5m"
sample_store = true          # false = nessun file di storico
sample_interval = "10s"
retention_days = 31
data_dir = ""                # vuoto = ~/.local/share/powcon-plus
single_instance = true
start_window = false
icon_mode = "pixmap"         # pixmap (generate) | name (icona di tema)
```

Override da ambiente: `POWCON_CONFIG`, `POWCON_DATA_DIR`, `POWCON_SENSOR`,
`POWCON_LABEL_MODE` (utili per test e per far girare il daemon senza toccare
`$HOME`).

## 6. Interfaccia a riga di comando

```
powcon-plus run [--no-tray] [--no-store] [--verbose]   daemon + tray (default)
powcon-plus status [--json]        chiede il snapshot al daemon; se non c'è, legge l'hwmon
powcon-plus report [--days N] [--csv]                  kWh/€/media/picco per giorno
powcon-plus window                 finestra Fyne (binario costruito con -tags gui)
powcon-plus ctl status|pause|resume|label-mode mode=both|alert w=800|quit
powcon-plus sensors                canali hwmon rilevati (diagnostica)
powcon-plus install-autostart [--mode xdg|systemd] [--exec PATH]
powcon-plus uninstall-autostart | autostart-status
powcon-plus uninstall [--purge]      fermo daemon + autostart + binari (+ dati/config)
powcon-plus init-config | version
```

## 7. Avvio automatico

L'attivazione avviene **dall'interfaccia**, come da requisito: sia dal menu
dell'icona di tray ("Avvio automatico → All'avvio della sessione") sia dalla
finestra di dettaglio (spunta "Avvio automatico"), che interrogano il daemon
sul socket di controllo:

```
ctl autostart-status  → { xdg, path, unit, unit_path }
ctl autostart-set     (args: { enabled: bool })
```

Il daemon delega a `internal/autostart`, che scrive
`~/.config/autostart/powcon-plus.desktop` con `Exec=<eseguibile corrente> run`
e `X-GNOME-Autostart-Delay=4`: l'autostart si aggancia alla **sessione
grafica**, condizione necessaria per l'icona di tray (serve il bus di
sessione). Alternativa da CLI `powcon-plus install-autostart --mode systemd`:
unit utente con `PartOf=graphical-session.target`, utile per i log in
`journalctl --user`; l'icona compare comunque solo dentro la sessione
grafica. Il flock su `lock` evita doppioni se autostart e lancio manuale
coesistono. Il file `.desktop` usa il percorso dell'eseguibile attivo al
momento del toggle, quindi le due strade restano coerenti.

## 8. Test ed evidenze

- `internal/tray/tray_test.go`: giro completo **sul bus di sessione reale** —
  `Get`/`GetAll` delle proprietà SNI, `IconPixmap` (2 pixmap 22/44 px,
  `22×22×4` byte), `GetLayout` con firma `(ia{sv}av)`, `GetGroupProperties`
  con le label, `Event("clicked")` che invoca l'handler.
- `internal/psu`: mappatura per **etichette** (regge se il driver riordina i
  canali), scale unitarie, hwmon mancante.
- `internal/energy`: integrazione trapezio, buchi non integrati, rollup
  giornalieri ordinati, finestra dello storico.
- `internal/store`: round-trip atomico, append/lettura JSONL, retention,
  lock esclusivo.
- `internal/gui`: smoke test headless (`fyne.io/fyne/v2/test`) su snapshot
  sintetico, tabella 14 giorni, grafico con storico vuoto.
- Verifica live: daemon registrato al watcher →
  `RegisteredStatusNotifierItems` contiene `org.kde.StatusNotifierItem-<pid>-1`,
  `XAyatanaLabel = "346 W · 3 Wh"`, `Menu = /MenuBar`.

## 9. Limiti noti

1. Il device USB può essere acquisito da OpenLinkHub/liquidctl/iCUE: in quel
   caso l'hwmon sparisce e il daemon passa in stato "alert" con etichetta `n/d`
   (e notifica all'avvio se il sensore non c'è).
2. `power total` è l'ingresso AC: include l'efficienza del PSU (~85-92% a
   questi carichi) e tutto ciò che è alimentato dal PSU; il rail 5VSB (standby)
   non è esposto, quindi la somma dei rail non torna con l'AC.
3. `XAyatanaLabel` non è standard: su GNOME/Ubuntu e su desktop Ayatana il
   testo è visibile, su KDE/Wayland-compositor nudi può comparire solo al
   passaggio del mouse.
4. Se il daemon parte prima che GNOME sia pronto, la registrazione al watcher è
   ritentata con backoff esponenziale (fino a ~30 tentativi).
5. La finestra GUI richiede OpenGL (cgo): il daemon no. `make` produce entrambi.

## 10. Sviluppo

```
make            # bin/powcon-plus (senza cgo) + bin/powcon-plus-gui (Fyne)
make test vet fmt
make install install-autostart
```

Roadmap ragionevole:

- **v0.2**: attribuzione stimata per componente (cpu `amdgpu` PPT/junction,
  GPU, NVMe, fan) correlata alla potenza AC; tariffe multi-orarie e costo per
  fascia; export CSV/Prometheus (`/metrics` opzionale) e dashboard web.
- **v0.3**: profili di consumo (lavoro/rendering/sleep), soglia dinamica
  percentile, report settimanale pianificato con timer systemd, supporto altri PSU
  HID (HX/AX serie i, Hydro X?), impostazione della curva ventola
  dove il firmware lo consente.
