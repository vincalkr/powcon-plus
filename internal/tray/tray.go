// Package tray implements a native Linux system tray icon (KDE
// StatusNotifierItem + DBusMenu) in pure Go, without cgo and without GTK.
//
// Compatibility notes:
//   - the host (GNOME with the AppIndicator extension, KDE, XFCE, Hyprland/swaybar)
//     discovers the icon through org.kde.StatusNotifierWatcher;
//   - the text next to the icon in GNOME goes through the non-standard
//     XAyatanaLabel property (ubuntu-appindicators / KStatusNotifierItem extension);
//     on KDE the text is shown by the panel's Title/Label field.
package tray

import (
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	ifaceSNI     = "org.kde.StatusNotifierItem"
	ifaceWatcher = "org.kde.StatusNotifierWatcher"
	ifaceDBus    = "org.freedesktop.DBus"
	ifaceProps   = "org.freedesktop.DBus.Properties"

	watcherPath = "/StatusNotifierWatcher"
)

// Pixmap is one entry of IconPixmap: ARGB32 in network byte order.
type Pixmap struct {
	W    int32
	H    int32
	Data []byte
}

// Options configures the item.
type Options struct {
	ID       string // e.g. "powcon-plus"
	Title    string // human-readable title, used by screen readers/tooltips
	Path     dbus.ObjectPath
	MenuPath dbus.ObjectPath
	Conn     *dbus.Conn // optional, defaults to the session bus
}

// Item is a StatusNotifierItem exposed on the session bus.
type Item struct {
	conn     *dbus.Conn
	busName  string
	path     dbus.ObjectPath
	menuPath dbus.ObjectPath
	menu     *Menu
	pal      IconPalette
	state    IconState

	mu    sync.RWMutex
	props map[string]dbus.Variant

	onActivate          func(x, y int32)
	onSecondaryActivate func(x, y int32)
	onContextMenu       func(x, y int32)
	onScroll            func(delta int32, orientation string)
}

// New creates (but does not register) the item.
func New(o Options) (*Item, error) {
	if o.Path == "" {
		o.Path = "/StatusNotifierItem"
	}
	if o.MenuPath == "" {
		o.MenuPath = "/MenuBar"
	}
	if o.ID == "" {
		o.ID = "powcon-plus"
	}
	conn := o.Conn
	if conn == nil {
		c, err := dbus.SessionBus()
		if err != nil {
			return nil, fmt.Errorf("bus di sessione: %w", err)
		}
		conn = c
	}
	it := &Item{
		conn:     conn,
		busName:  fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid()),
		path:     o.Path,
		menuPath: o.MenuPath,
		props:    map[string]dbus.Variant{},
	}
	it.menu = newMenu(conn, o.MenuPath)
	it.pal = DefaultPalette()
	it.state = IconState(-1) // force publication of the first icon

	// properties required by the interface used by the GNOME extension
	it.props["Category"] = dbus.MakeVariant("ApplicationStatus")
	it.props["Id"] = dbus.MakeVariant(o.ID)
	it.props["Title"] = dbus.MakeVariant(o.Title)
	it.props["Status"] = dbus.MakeVariant("Active")
	it.props["WindowId"] = dbus.MakeVariant(int32(0))
	it.props["IconThemePath"] = dbus.MakeVariant("")
	it.props["Menu"] = dbus.MakeVariant(o.MenuPath)
	it.props["ItemIsMenu"] = dbus.MakeVariant(false)
	it.props["IconName"] = dbus.MakeVariant("")
	it.props["IconPixmap"] = dbus.MakeVariant([]Pixmap{})
	it.props["OverlayIconName"] = dbus.MakeVariant("")
	it.props["OverlayIconPixmap"] = dbus.MakeVariant([]Pixmap{})
	it.props["AttentionIconName"] = dbus.MakeVariant("")
	it.props["AttentionIconPixmap"] = dbus.MakeVariant([]Pixmap{})
	it.props["AttentionMovieName"] = dbus.MakeVariant("")
	it.props["ToolTip"] = dbus.MakeVariant(toolTip{})
	// Ayatana properties: make the text appear in the GNOME bar
	it.props["XAyatanaLabel"] = dbus.MakeVariant("")
	it.props["XAyatanaLabelGuide"] = dbus.MakeVariant("")
	it.props["XAyatanaOrderingIndex"] = dbus.MakeVariant(uint32(0))
	it.props["IconAccessibleDesc"] = dbus.MakeVariant(o.Title)
	it.props["AttentionAccessibleDesc"] = dbus.MakeVariant("")

	if err := conn.Export(&sniMethods{it}, o.Path, ifaceSNI); err != nil {
		return nil, err
	}
	if err := conn.Export(&propsHandler{it}, o.Path, ifaceProps); err != nil {
		return nil, err
	}
	if err := conn.Export(introspectMethods{}, o.Path, "org.freedesktop.Introspectable"); err != nil {
		return nil, err
	}
	if err := conn.Export(&menuMethods{it.menu}, o.MenuPath, ifaceMenu); err != nil {
		return nil, err
	}
	if err := conn.Export(&menuProps{it}, o.MenuPath, ifaceProps); err != nil {
		return nil, err
	}
	return it, nil
}

// SetPalette sets the glyph colors.
func (it *Item) SetPalette(pal IconPalette) {
	it.mu.Lock()
	it.pal = pal
	it.mu.Unlock()
}

// SetIconState changes the glyph appearance (normal / alert / paused).
func (it *Item) SetIconState(s IconState) {
	it.mu.Lock()
	pal := it.pal
	if s == it.state {
		it.mu.Unlock()
		return
	}
	it.state = s
	it.mu.Unlock()
	it.SetIcon(Bolt(pal, s))
}

// IconState returns the current glyph state.
func (it *Item) IconState() IconState {
	it.mu.RLock()
	defer it.mu.RUnlock()
	return it.state
}

// Menu returns the context menu tree.
func (it *Item) Menu() *Menu { return it.menu }

// OnActivate sets the primary-click handler.
func (it *Item) OnActivate(f func(x, y int32)) { it.onActivate = f }

// OnSecondaryActivate sets the secondary/middle-click handler.
func (it *Item) OnSecondaryActivate(f func(x, y int32)) { it.onSecondaryActivate = f }

// OnContextMenu sets the context-menu request handler.
func (it *Item) OnContextMenu(f func(x, y int32)) { it.onContextMenu = f }

// OnScroll sets the scroll-wheel handler on the icon.
func (it *Item) OnScroll(f func(delta int32, orientation string)) { it.onScroll = f }

// SetLabel sets the text shown next to the icon and, for hosts that use it,
// the accessible title/description.
func (it *Item) SetLabel(label string) {
	it.mu.Lock()
	changed := map[string]dbus.Variant{}
	if v := it.props["XAyatanaLabel"]; v.String() != dbus.MakeVariant(label).String() {
		it.props["XAyatanaLabel"] = dbus.MakeVariant(label)
		changed["XAyatanaLabel"] = dbus.MakeVariant(label)
	}
	if v := it.props["Title"]; v.String() != dbus.MakeVariant(label).String() {
		it.props["Title"] = dbus.MakeVariant(label)
		changed["Title"] = dbus.MakeVariant(label)
	}
	if v := it.props["IconAccessibleDesc"]; v.String() != dbus.MakeVariant(label).String() {
		it.props["IconAccessibleDesc"] = dbus.MakeVariant(label)
		changed["IconAccessibleDesc"] = dbus.MakeVariant(label)
	}
	it.mu.Unlock()
	it.emitProps(changed)
}

// SetIcon publishes a new icon (pixmap).
func (it *Item) SetIcon(px []Pixmap) {
	it.mu.Lock()
	it.props["IconPixmap"] = dbus.MakeVariant(px)
	changed := map[string]dbus.Variant{"IconPixmap": dbus.MakeVariant(px)}
	it.mu.Unlock()
	it.emitProps(changed)
}

// SetIconName uses a theme icon (as an alternative to pixmaps).
func (it *Item) SetIconName(name, themePath string) {
	it.mu.Lock()
	it.props["IconName"] = dbus.MakeVariant(name)
	it.props["IconThemePath"] = dbus.MakeVariant(themePath)
	changed := map[string]dbus.Variant{
		"IconName":      dbus.MakeVariant(name),
		"IconThemePath": dbus.MakeVariant(themePath),
	}
	it.mu.Unlock()
	it.emitProps(changed)
}

// SetStatus sets Active/Passive/NeedsAttention.
func (it *Item) SetStatus(status string) {
	it.mu.Lock()
	it.props["Status"] = dbus.MakeVariant(status)
	changed := map[string]dbus.Variant{"Status": dbus.MakeVariant(status)}
	it.mu.Unlock()
	it.emitProps(changed)
}

// SetToolTip sets the tooltip title/description.
func (it *Item) SetToolTip(title, description string) {
	tt := toolTip{Path: "", Pixmap: []Pixmap{}, Title: title, Description: description}
	it.mu.Lock()
	it.props["ToolTip"] = dbus.MakeVariant(tt)
	changed := map[string]dbus.Variant{"ToolTip": dbus.MakeVariant(tt)}
	it.mu.Unlock()
	it.emitProps(changed)
}

func (it *Item) get(key string) (dbus.Variant, bool) {
	it.mu.RLock()
	defer it.mu.RUnlock()
	v, ok := it.props[key]
	return v, ok
}

func (it *Item) all() map[string]dbus.Variant {
	it.mu.RLock()
	defer it.mu.RUnlock()
	out := make(map[string]dbus.Variant, len(it.props))
	for k, v := range it.props {
		out[k] = v
	}
	return out
}

func (it *Item) emitProps(changed map[string]dbus.Variant) {
	if len(changed) == 0 {
		return
	}
	if err := it.conn.Emit(it.path, ifaceProps+".PropertiesChanged",
		ifaceSNI, changed, []string{}); err != nil {
		fmt.Fprintf(os.Stderr, "tray: PropertiesChanged: %v\n", err)
	}
}

// Register asks the watcher to host the icon.
func (it *Item) Register() error {
	reply := it.conn.Object(ifaceWatcher, watcherPath).
		Call(ifaceWatcher+".RegisterStatusNotifierItem", 0, it.busName)
	return reply.Store()
}

// ClaimName acquires the name on the bus (idempotent).
func (it *Item) ClaimName() error {
	reply, err := it.conn.RequestName(it.busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("nome %s già acquisito", it.busName)
	}
	return nil
}

func unknownInterface(iface string) *dbus.Error {
	return &dbus.Error{
		Name: "org.freedesktop.DBus.Error.UnknownInterface",
		Body: []interface{}{fmt.Sprintf("oggetto senza interfaccia %s", iface)},
	}
}

// toolTip mirrors the (sa(iiay)ss) type of the ToolTip property.
type toolTip struct {
	Path        string
	Pixmap      []Pixmap
	Title       string
	Description string
}

// sniMethods exposes the methods of the SNI interface.
type sniMethods struct{ it *Item }

// introspectMethods implements org.freedesktop.Introspectable on the SNI
// object. The GNOME appindicator extension calls Introspect to detect
// method support (e.g. Activate); without it left-click is ignored.
type introspectMethods struct{}

func (introspectMethods) Introspect() (string, *dbus.Error) {
	return sniIntrospectionXML, nil
}

const sniIntrospectionXML = `
<node>
  <interface name="org.kde.StatusNotifierItem">
    <property name="Category" type="s" access="read"/>
    <property name="Id" type="s" access="read"/>
    <property name="Title" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="WindowId" type="i" access="read"/>
    <property name="IconThemePath" type="s" access="read"/>
    <property name="Menu" type="o" access="read"/>
    <property name="ItemIsMenu" type="b" access="read"/>
    <property name="IconName" type="s" access="read"/>
    <property name="IconAccessibleDesc" type="s" access="read"/>
    <property name="IconPixmap" type="a(iiay)" access="read"/>
    <property name="OverlayIconName" type="s" access="read"/>
    <property name="OverlayIconPixmap" type="a(iiay)" access="read"/>
    <property name="AttentionIconName" type="s" access="read"/>
    <property name="AttentionIconPixmap" type="a(iiay)" access="read"/>
    <property name="AttentionMovieName" type="s" access="read"/>
    <property name="AttentionAccessibleDesc" type="s" access="read"/>
    <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
    <property name="XAyatanaLabel" type="s" access="read"/>
    <property name="XAyatanaLabelGuide" type="s" access="read"/>
    <property name="XAyatanaOrderingIndex" type="u" access="read"/>
    <method name="ContextMenu">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
    </method>
    <method name="Activate">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
    </method>
    <method name="SecondaryActivate">
      <arg name="x" type="i" direction="in"/>
      <arg name="y" type="i" direction="in"/>
    </method>
    <method name="XAyatanaSecondaryActivate">
      <arg name="timestamp" type="u" direction="in"/>
    </method>
    <method name="Scroll">
      <arg name="delta" type="i" direction="in"/>
      <arg name="orientation" type="s" direction="in"/>
    </method>
    <method name="ProvideXdgActivationToken">
      <arg name="token" type="s" direction="in"/>
    </method>
  </interface>
</node>
`

func (s *sniMethods) Activate(x, y int32) *dbus.Error {
	if s.it.onActivate != nil {
		go s.it.onActivate(x, y)
	}
	return nil
}

func (s *sniMethods) SecondaryActivate(x, y int32) *dbus.Error {
	if s.it.onSecondaryActivate != nil {
		go s.it.onSecondaryActivate(x, y)
	}
	return nil
}

// XAyatanaSecondaryActivate is the ayatana-flavored middle-click variant
// (timestamp only); the GNOME extension prefers it when present.
func (s *sniMethods) XAyatanaSecondaryActivate(timestamp uint32) *dbus.Error {
	if s.it.onSecondaryActivate != nil {
		go s.it.onSecondaryActivate(0, 0)
	}
	return nil
}

func (s *sniMethods) ContextMenu(x, y int32) *dbus.Error {
	if s.it.onContextMenu != nil {
		go s.it.onContextMenu(x, y)
	}
	return nil
}

func (s *sniMethods) ProvideXdgActivationToken(token string) *dbus.Error {
	return nil
}

func (s *sniMethods) Scroll(delta int32, orientation string) *dbus.Error {
	if s.it.onScroll != nil {
		go s.it.onScroll(delta, orientation)
	}
	return nil
}

// propsHandler serves org.freedesktop.DBus.Properties on the SNI object.
type propsHandler struct{ it *Item }

func (p *propsHandler) Get(iface, key string) (dbus.Variant, *dbus.Error) {
	if iface != ifaceSNI {
		return dbus.Variant{}, unknownInterface(iface)
	}
	v, ok := p.it.get(key)
	if !ok {
		return dbus.Variant{}, &dbus.Error{
			Name: "org.freedesktop.DBus.Error.UnknownProperty",
			Body: []interface{}{"proprietà sconosciuta " + key},
		}
	}
	return v, nil
}

func (p *propsHandler) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != ifaceSNI {
		return nil, unknownInterface(iface)
	}
	return p.it.all(), nil
}

func (p *propsHandler) Set(iface, key string, v dbus.Variant) *dbus.Error {
	return &dbus.Error{
		Name: "org.freedesktop.DBus.Error.PropertyReadOnly",
		Body: []interface{}{key + " è in sola lettura"},
	}
}
