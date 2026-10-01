package tray

import (
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

// ifaceMenu is the (canonical) DBusMenu interface, used by GNOME/KDE/XFCE.
const ifaceMenu = "com.canonical.dbusmenu"

// Menu is a DBusMenu context menu. It is rebuilt with Rebuild: numeric ids
// stay stable per key, so the host updates without flicker.
type Menu struct {
	conn  *dbus.Conn
	path  dbus.ObjectPath
	root  *menuNode
	hook  map[int32]func()
	numID map[string]int32
	next  int32
	rev   uint32

	// lastShownRev is the revision the host has seen through AboutToShow:
	// the host re-fetches the layout only when we report a change there.
	lastShownRev uint32

	mu sync.Mutex
}

type menuNode struct {
	id       int32
	kind     string // standard | separator
	label    string
	enabled  bool
	visible  bool
	toggle   string // "" | checkmark | radio
	state    int32  // 0 off, 1 on
	children []*menuNode
}

func newMenu(conn *dbus.Conn, path dbus.ObjectPath) *Menu {
	return &Menu{
		conn:  conn,
		path:  path,
		root:  &menuNode{id: 0, kind: "standard", enabled: true, visible: true},
		hook:  map[int32]func(){},
		numID: map[string]int32{},
		next:  1,
	}
}

// MenuBuilder builds a branch of the menu.
type MenuBuilder struct {
	m   *Menu
	cur *menuNode
}

// Item adds a regular entry.
func (b *MenuBuilder) Item(key, label string, onClick func()) *MenuBuilder {
	return b.add(key, label, "", false, onClick)
}

// Check adds an entry with a check box.
func (b *MenuBuilder) Check(key, label string, checked bool, onClick func()) *MenuBuilder {
	return b.add(key, label, "checkmark", checked, onClick)
}

// Radio adds an entry of a radio group.
func (b *MenuBuilder) Radio(key, label string, active bool, onClick func()) *MenuBuilder {
	return b.add(key, label, "radio", active, onClick)
}

func (b *MenuBuilder) add(key, label, toggle string, on bool, onClick func()) *MenuBuilder {
	n := &menuNode{
		id:      b.m.idOf(key),
		kind:    "standard",
		label:   label,
		enabled: true,
		visible: true,
		toggle:  toggle,
	}
	if on {
		n.state = 1
	}
	if onClick != nil {
		b.m.hook[n.id] = onClick
	}
	b.cur.children = append(b.cur.children, n)
	return b
}

// Disabled adds a non-clickable entry (informational text).
func (b *MenuBuilder) Disabled(label string) *MenuBuilder {
	b.cur.children = append(b.cur.children, &menuNode{
		id: b.m.idOf("info-" + label), kind: "standard", label: label,
		enabled: false, visible: true,
	})
	return b
}

// Separator adds a separator.
func (b *MenuBuilder) Separator() *MenuBuilder {
	b.cur.children = append(b.cur.children, &menuNode{
		id: b.m.idOf("sep-" + fmt.Sprint(len(b.cur.children))), kind: "separator",
		enabled: true, visible: true,
	})
	return b
}

// Sub opens a submenu.
func (b *MenuBuilder) Sub(key, label string, fn func(*MenuBuilder)) *MenuBuilder {
	n := &menuNode{
		id: b.m.idOf(key), kind: "standard", label: label,
		enabled: true, visible: true,
	}
	b.cur.children = append(b.cur.children, n)
	if fn != nil {
		fn(&MenuBuilder{m: b.m, cur: n})
	}
	return b
}

func (m *Menu) idOf(key string) int32 {
	if id, ok := m.numID[key]; ok {
		return id
	}
	id := m.next
	m.next++
	m.numID[key] = id
	return id
}

// Rebuild replaces the menu content and notifies the host.
func (m *Menu) Rebuild(fn func(b *MenuBuilder)) {
	m.mu.Lock()
	m.hook = map[int32]func(){}
	root := &menuNode{id: 0, kind: "standard", enabled: true, visible: true}
	m.root = root
	fn(&MenuBuilder{m: m, cur: root})
	m.rev++
	rev := m.rev
	m.mu.Unlock()
	m.emitLayoutUpdated(rev)
}

func (m *Menu) emitLayoutUpdated(rev uint32) {
	// Hosts disagree on the signal name: the Ubuntu AppIndicator extension
	// listens for LayoutUpdated, the canonical DBusMenu hosts (KDE, XFCE,
	// swaybar) for LayoutChanged. Emit both so everyone can refresh.
	for _, name := range []string{ifaceMenu + ".LayoutUpdated", ifaceMenu + ".LayoutChanged"} {
		if err := m.conn.Emit(m.path, name, rev, int32(0)); err != nil {
			fmt.Fprintf(os.Stderr, "tray: %s: %v\n", name, err)
		}
	}
}

// --- D-Bus implementation (dedicated types: interface methods only) ---

type menuMethods struct{ m *Menu }

type layoutProps struct {
	V0 int32
	V1 map[string]dbus.Variant
}

// menuEvent mirrors the a(isvu) tuple of EventGroup.
type menuEvent struct {
	V0 int32
	V1 string
	V2 dbus.Variant
	V3 uint32
}

type layoutNode struct {
	V0 int32
	V1 map[string]dbus.Variant
	V2 []dbus.Variant
}

// GetLayout returns (revision, layout). The layout must be returned as a
// plain (ia{sv}av) struct — not wrapped in a variant — because the GNOME
// appindicator extension validates the reply signature strictly and drops
// the whole menu otherwise.
func (mm *menuMethods) GetLayout(parent int32, recursionDepth int32, propertyNames []string) (uint32, layoutNode, *dbus.Error) {
	mm.m.mu.Lock()
	defer mm.m.mu.Unlock()
	node := mm.m.find(mm.m.root, parent)
	if node == nil {
		return mm.m.rev, layoutNode{V0: parent, V1: map[string]dbus.Variant{}}, nil
	}
	return mm.m.rev, mm.m.layoutOf(node, recursionDepth, propertyNames), nil
}

func (mm *menuMethods) GetGroupProperties(ids []int32, propertyNames []string) ([]layoutProps, *dbus.Error) {
	mm.m.mu.Lock()
	defer mm.m.mu.Unlock()
	var out []layoutProps
	if len(ids) == 0 {
		// by convention: all items
		var walk func(n *menuNode)
		walk = func(n *menuNode) {
			out = append(out, layoutProps{V0: n.id, V1: mm.m.propsOf(n, propertyNames)})
			for _, c := range n.children {
				walk(c)
			}
		}
		walk(mm.m.root)
		return out, nil
	}
	for _, id := range ids {
		if n := mm.m.find(mm.m.root, id); n != nil {
			out = append(out, layoutProps{V0: n.id, V1: mm.m.propsOf(n, propertyNames)})
		}
	}
	return out, nil
}

func (mm *menuMethods) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	mm.m.mu.Lock()
	defer mm.m.mu.Unlock()
	n := mm.m.find(mm.m.root, id)
	if n == nil {
		return dbus.Variant{}, &dbus.Error{
			Name: "org.freedesktop.DBus.Error.UnknownMethod",
			Body: []interface{}{"no such item"},
		}
	}
	p := mm.m.propsOf(n, nil)
	v, ok := p[name]
	if !ok {
		return dbus.MakeVariant(""), nil
	}
	return v, nil
}

func (mm *menuMethods) Event(id int32, eventId string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventId != "clicked" {
		return nil
	}
	mm.m.mu.Lock()
	fn := mm.m.hook[id]
	mm.m.mu.Unlock()
	if fn != nil {
		go fn()
	}
	return nil
}

func (mm *menuMethods) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	var errs []int32
	for _, ev := range events {
		if ev.V1 == "clicked" {
			if e := mm.Event(ev.V0, ev.V1, ev.V2, ev.V3); e != nil {
				errs = append(errs, ev.V0)
			}
		}
	}
	return errs, nil
}

// AboutToShow reports whether the layout changed since the host last asked
// to show the menu. Hosts re-fetch the layout only when we return true, so
// without this their cached copy (labels, check marks) would go stale.
func (mm *menuMethods) AboutToShow(id int32) (bool, *dbus.Error) {
	mm.m.mu.Lock()
	changed := mm.m.rev != mm.m.lastShownRev
	if changed {
		mm.m.lastShownRev = mm.m.rev
	}
	mm.m.mu.Unlock()
	return changed, nil
}

func (mm *menuMethods) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

// --- helper ---

func (m *Menu) find(n *menuNode, id int32) *menuNode {
	if n == nil {
		return nil
	}
	if n.id == id {
		return n
	}
	for _, c := range n.children {
		if f := m.find(c, id); f != nil {
			return f
		}
	}
	return nil
}

func (m *Menu) propsOf(n *menuNode, only []string) map[string]dbus.Variant {
	want := func(name string) bool {
		if len(only) == 0 {
			return true
		}
		for _, o := range only {
			if o == name {
				return true
			}
		}
		return false
	}
	out := map[string]dbus.Variant{}
	if want("label") {
		out["label"] = dbus.MakeVariant(n.label)
	}
	if want("enabled") {
		out["enabled"] = dbus.MakeVariant(n.enabled)
	}
	if want("visible") {
		out["visible"] = dbus.MakeVariant(n.visible)
	}
	if want("type") {
		out["type"] = dbus.MakeVariant(n.kind)
	}
	if want("children-display") && len(n.children) > 0 {
		out["children-display"] = dbus.MakeVariant("submenu")
	}
	if want("toggle-type") && n.toggle != "" {
		out["toggle-type"] = dbus.MakeVariant(n.toggle)
	}
	if want("toggle-state") && n.toggle != "" {
		out["toggle-state"] = dbus.MakeVariant(n.state)
	}
	return out
}

func (m *Menu) layoutOf(n *menuNode, depth int32, only []string) layoutNode {
	out := layoutNode{V0: n.id, V1: m.propsOf(n, only), V2: []dbus.Variant{}}
	if depth == 0 {
		return out
	}
	for _, c := range n.children {
		next := depth - 1
		if depth < 0 {
			next = depth
		}
		out.V2 = append(out.V2, dbus.MakeVariant(m.layoutOf(c, next, only)))
	}
	return out
}

// menuProps serves the properties required by the DBusMenu interface.
type menuProps struct{ it *Item }

func (p *menuProps) Get(iface, key string) (dbus.Variant, *dbus.Error) {
	if iface != ifaceMenu {
		return dbus.Variant{}, unknownInterface(iface)
	}
	switch key {
	case "Version":
		return dbus.MakeVariant(uint32(0)), nil
	case "TextDirection":
		return dbus.MakeVariant("ltr"), nil
	case "Status":
		return dbus.MakeVariant("show"), nil
	case "IconThemePath":
		return dbus.MakeVariant([]string{}), nil
	}
	return dbus.Variant{}, &dbus.Error{
		Name: "org.freedesktop.DBus.Error.UnknownProperty",
		Body: []interface{}{key},
	}
}

func (p *menuProps) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != ifaceMenu {
		return nil, unknownInterface(iface)
	}
	return map[string]dbus.Variant{
		"Version":       dbus.MakeVariant(uint32(0)),
		"TextDirection": dbus.MakeVariant("ltr"),
		"Status":        dbus.MakeVariant("show"),
		"IconThemePath": dbus.MakeVariant([]string{}),
	}, nil
}

func (p *menuProps) Set(iface, key string, v dbus.Variant) *dbus.Error {
	return &dbus.Error{
		Name: "org.freedesktop.DBus.Error.PropertyReadOnly",
		Body: []interface{}{key},
	}
}
