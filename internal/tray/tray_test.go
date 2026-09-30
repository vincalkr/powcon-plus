package tray

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// TestExportRoundTrip verifies that the wire encoding of SNI and DBusMenu is
// correct: it exports the item on the session bus and calls back on itself.
// It is skipped when there is no session bus (headless CI).
func TestExportRoundTrip(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("nessun bus di sessione")
	}
	it, err := New(Options{ID: "powcon-test", Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := it.ClaimName(); err != nil {
		t.Fatal(err)
	}
	defer it.conn.ReleaseName(it.busName)

	item := it.conn.Object(it.busName, it.path)

	// SNI properties
	var label string
	if err := item.Call(ifaceProps+".Get", 0, ifaceSNI, "XAyatanaLabel").Store(&label); err != nil {
		t.Fatalf("Get XAyatanaLabel: %v", err)
	}
	it.SetLabel("123 W")
	if err := item.Call(ifaceProps+".Get", 0, ifaceSNI, "XAyatanaLabel").Store(&label); err != nil {
		t.Fatalf("Get XAyatanaLabel dopo SetLabel: %v", err)
	}
	if label != "123 W" {
		t.Fatalf("etichetta = %q, atteso '123 W'", label)
	}

	var all map[string]dbus.Variant
	if err := item.Call(ifaceProps+".GetAll", 0, ifaceSNI).Store(&all); err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	for _, want := range []string{"Category", "Id", "Title", "Status", "WindowId", "Menu",
		"ItemIsMenu", "IconName", "IconPixmap", "IconThemePath", "XAyatanaLabel"} {
		if _, ok := all[want]; !ok {
			t.Errorf("proprietà %s assente da GetAll", want)
		}
	}

	// icon
	it.SetIconState(StateNormal)
	var px []Pixmap
	if err := item.Call(ifaceProps+".Get", 0, ifaceSNI, "IconPixmap").Store(&px); err != nil {
		t.Fatalf("Get IconPixmap: %v", err)
	}
	if len(px) != 2 || px[0].W != 22 || len(px[0].Data) != 22*22*4 {
		t.Fatalf("IconPixmap inattesa: %+v", px)
	}

	// menu
	it.Menu().Rebuild(func(b *MenuBuilder) {
		b.Disabled("140 W")
		b.Separator()
		b.Item("win", "Apri dettaglio", func() {})
		b.Check("pause", "Pausa", true, func() {})
		b.Sub("sub", "Etichetta", func(sb *MenuBuilder) {
			sb.Radio("a", "Potenza", true, func() {})
		})
	})
	menu := it.conn.Object(it.busName, it.menuPath)
	var rev uint32
	var layout dbus.Variant
	if err := menu.Call(ifaceMenu+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &layout); err != nil {
		t.Fatalf("GetLayout: %v", err)
	}
	if layout.Signature().String() != "(ia{sv}av)" {
		t.Fatalf("firma GetLayout = %s", layout.Signature().String())
	}
	var props []layoutProps
	if err := menu.Call(ifaceMenu+".GetGroupProperties", 0, []int32{}, []string{}).Store(&props); err != nil {
		t.Fatalf("GetGroupProperties: %v", err)
	}
	if len(props) < 5 {
		t.Fatalf("GetGroupProperties ha restituito %d item", len(props))
	}
	foundLabel := false
	for _, p := range props {
		if v, ok := p.V1["label"]; ok {
			t.Logf("item %d label=%v props=%d", p.V0, v.Value(), len(p.V1))
		}
		if v, ok := p.V1["label"]; ok && fmt.Sprint(v.Value()) == "Apri dettaglio" {
			foundLabel = true
		}
	}
	if !foundLabel {
		t.Error("label 'Apri dettaglio' non trovato nelle proprietà del menu")
	}

	// click on an entry
	var clicked []int32
	done := make(chan int32, 1)
	it.Menu().Rebuild(func(b *MenuBuilder) {
		b.Item("hit", "Premi", func() { done <- 1 })
	})
	if err := menu.Call(ifaceMenu+".Event", 0, idOfKey(it.Menu(), "hit"), "clicked",
		dbus.MakeVariant(int32(0)), uint32(0)).Store(); err != nil {
		t.Fatalf("Event: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler del click non invocato")
	}
	_ = clicked
}

func idOfKey(m *Menu, key string) int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idOf(key)
}

func TestBoltPixmap(t *testing.T) {
	px := Bolt(DefaultPalette(), StateNormal, 22)
	if len(px) != 1 {
		t.Fatalf(" pixmap = %d", len(px))
	}
	if len(px[0].Data) != 22*22*4 {
		t.Fatalf("dimensione dati = %d", len(px[0].Data))
	}
	filled := 0
	for i := 0; i < len(px[0].Data); i += 4 {
		if px[0].Data[i] != 0 {
			filled++
		}
	}
	if filled < 60 || filled > 22*22 {
		t.Fatalf("pixel coperti = %d (atteso ~100)", filled)
	}
	// the alpha channel must precede RGB (network byte order)
	var firstNonEmpty = -1
	for i := 0; i < len(px[0].Data); i += 4 {
		if px[0].Data[i] == 255 {
			firstNonEmpty = i
			break
		}
	}
	if firstNonEmpty < 0 || px[0].Data[firstNonEmpty+1] == 0 {
		t.Fatal("bytes ARGB inattesi")
	}
}
