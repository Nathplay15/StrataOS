package i18n

import "testing"

func TestTranslate(t *testing.T) {
	cases := []struct{ lang, in, want string }{
		{"en", "Le volume data est plein à 93 %", "Volume data is 93% full"},
		{"fr", "Volume data is 93% full", "Le volume data est plein à 93 %"},
		{"en", "unknown text", "unknown text"},
		{"en", "Sauvegarde : nuit : terminé", "Backup: nuit: finished"},
		{"fr", "Backup: night: finished", "Sauvegarde : night : terminé"},
	}
	for _, c := range cases {
		if got := T(c.lang, c.in); got != c.want {
			t.Errorf("T(%s, %q) = %q, want %q", c.lang, c.in, got, c.want)
		}
	}
	if got := Lines("fr", "Storage\n• data: 1.0 GB used of 2.0 TB (50%)"); got != "Stockage\n• data : 1.0 Go utilisés sur 2.0 To (50 %)" {
		t.Errorf("Lines = %q", got)
	}
	if got := Lines("fr", "💾 Storage\n+ Storage"); got != "💾 Stockage\n+ Storage" {
		t.Errorf("emoji heading: %q", got)
	}
	if got := Lines("de", "Monthly report of nas"); got == "Monthly report of nas" {
		t.Errorf("German report subject not translated: %q", got)
	}
	if got := T("de", "Volume data is 93% full"); got == "Volume data is 93% full" {
		t.Errorf("German not applied: %q", got)
	}
}

// Unknown sentences are never translated piece by piece through templates
// made of connectors or of a single word.
func TestNoPartialTranslation(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Folder to create", "Folder to create"},
		{"Share every holiday picture with grandma", "Share every holiday picture with grandma"},
		{"Update failed because the disk is full", "Update failed because the disk is full"},
		{"error: could not reach the server", "error: could not reach the server"},
		{"a, b", "a, b"},
		{"Backup: night: finished", "Sauvegarde : night : terminé"},
		{"Install Nextcloud", "Installation de Nextcloud"},
	} {
		if got := T("fr", c.in); got != c.want {
			t.Errorf("T(fr, %q) = %q, want %q", c.in, got, c.want)
		}
	}
	for tpl, want := range map[string]string{"{0} to {1}": "connector", "{0}, {1}": "connector", "error: {0}": "connector", "Update {0}": "weak", "Backup: {0}": "weak", "Let's Encrypt certificate for {0}": "strong", "Done": "exact"} {
		if got := Kind(tpl); got != want {
			t.Errorf("Kind(%q) = %q, want %q", tpl, got, want)
		}
	}
	if got := Key("fr", "Backup: {0}", "night"); got != "Sauvegarde : night" {
		t.Errorf("Key = %q", got)
	}
}
