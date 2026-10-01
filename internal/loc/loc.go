// Package loc provides the embedded translation catalogs for the user-facing
// strings (detail window, tray menu, tooltips, notifications).
//
// Catalogs are flat key/value JSON files, one per language, embedded in the
// binary. English is the fallback: a key missing from any other catalog is
// resolved from en.json. The CLI is not localized (it is English only).
package loc

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

//go:embed *.json
var embedded embed.FS

// Tag identifies an available language.
type Tag struct {
	Code string
	Name string // native name, shown in the UI selector
}

// Tags lists the bundled languages, in UI order.
var Tags = []Tag{
	{Code: "it", Name: "Italiano"},
	{Code: "en", Name: "English"},
	{Code: "de", Name: "Deutsch"},
	{Code: "fr", Name: "Français"},
	{Code: "es", Name: "Español"},
	{Code: "zh", Name: "中文"},
	{Code: "ja", Name: "日本語"},
}

const fallbackCode = "en"

// Catalog is a loaded language catalog.
type Catalog struct {
	code string
	msgs map[string]string
}

var (
	cacheMu sync.RWMutex
	cache   = map[string]*Catalog{}
)

// Available returns the codes of the bundled languages.
func Available() []string {
	codes := make([]string, len(Tags))
	for i, t := range Tags {
		codes[i] = t.Code
	}
	return codes
}

// IsValid reports whether code is a bundled language.
func IsValid(code string) bool {
	for _, c := range Available() {
		if c == code {
			return true
		}
	}
	return false
}

// CodeOf maps a native name (as shown in the UI selector) back to its code.
func CodeOf(name string) string {
	for _, t := range Tags {
		if t.Name == name {
			return t.Code
		}
	}
	return ""
}

// NameOf returns the native name for a code ("?" when unknown).
func NameOf(code string) string {
	for _, t := range Tags {
		if t.Code == code {
			return t.Name
		}
	}
	return "?"
}

// Auto detects a language from the locale environment variables
// (LC_ALL, LC_MESSAGES, LANG). It returns "" when none of the bundled
// languages matches; the caller falls back to English.
func Auto() string {
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := os.Getenv(env)
		if v == "" {
			continue
		}
		// "it_IT.UTF-8" -> "it": strip the .suffix and the _region part.
		tag := strings.ToLower(strings.SplitN(strings.TrimSpace(v), ".", 2)[0])
		tag = strings.SplitN(tag, "_", 2)[0]
		if IsValid(tag) {
			return tag
		}
	}
	return ""
}

// Load returns the catalog for code (cached). An empty or unknown code
// resolves to English.
func Load(code string) *Catalog {
	if !IsValid(code) {
		code = fallbackCode
	}
	cacheMu.RLock()
	c, ok := cache[code]
	cacheMu.RUnlock()
	if ok {
		return c
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := cache[code]; ok {
		return c
	}
	msgs := map[string]string{}
	if b, err := embedded.ReadFile(code + ".json"); err == nil {
		_ = json.Unmarshal(b, &msgs)
	}
	c = &Catalog{code: code, msgs: msgs}
	cache[code] = c
	return c
}

// Code returns the catalog's language code.
func (c *Catalog) Code() string { return c.code }

// Get returns the translation of key, falling back to English and then to
// the key itself.
func (c *Catalog) Get(key string) string {
	if s, ok := c.msgs[key]; ok {
		return s
	}
	if c.code != fallbackCode {
		if s := Load(fallbackCode).msgs[key]; s != "" {
			return s
		}
	}
	return key
}

// Sprintf translates key and formats it with the given arguments.
func (c *Catalog) Sprintf(key string, args ...any) string {
	return fmt.Sprintf(c.Get(key), args...)
}
