// Package currency defines the display currencies for the cost estimate.
// The tariff is entered in the selected currency; no conversion is done.
package currency

// Supported lists the currencies offered in the UI, in display order.
var Supported = []string{"EUR", "USD", "GBP", "CNY", "JPY"}

// IsValid reports whether code is a supported currency.
func IsValid(code string) bool {
	for _, c := range Supported {
		if c == code {
			return true
		}
	}
	return false
}

// Symbol returns the display symbol for code (the ISO code itself as fallback).
func Symbol(code string) string {
	switch code {
	case "EUR":
		return "€"
	case "USD":
		return "$"
	case "GBP":
		return "£"
	case "CNY", "JPY":
		return "¥"
	}
	return code
}

// DefaultFor maps a UI language to its customary currency.
func DefaultFor(lang string) string {
	switch lang {
	case "en":
		return "USD"
	case "zh":
		return "CNY"
	case "ja":
		return "JPY"
	default: // it, de, fr, es and unknown
		return "EUR"
	}
}
