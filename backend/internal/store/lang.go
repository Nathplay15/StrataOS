package store

import "slices"

// langs are the languages of the interface (internal/i18n.Langs; the store
// cannot import it).
var langs = []string{"fr", "en", "es", "de", "it", "pt", "nl", "zh"}

// MessageLang is the language of the messages the NAS sends (notifications,
// health report): the one chosen in the settings, else the interface
// language of the first administrator (the one who set the NAS up, usually),
// else English.
func (st *State) MessageLang() string {
	if slices.Contains(langs, st.Settings.NotifyLang) {
		return st.Settings.NotifyLang
	}
	return st.AdminLang()
}

// AdminLang is the interface language of the oldest enabled administrator
// whose browser reported one ("en" when none did).
func (st *State) AdminLang() string {
	var first *User
	for _, u := range st.Users {
		if u.Role != "admin" || u.Disabled || !slices.Contains(langs, u.Lang) {
			continue
		}
		if first == nil || u.CreatedAt.Before(first.CreatedAt) {
			first = u
		}
	}
	if first == nil {
		return "en"
	}
	return first.Lang
}
