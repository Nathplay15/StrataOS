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
	if got := Lines("de", "Monthly report of nas"); got == "Monthly report of nas" {
		t.Errorf("German report subject not translated: %q", got)
	}
	if got := T("de", "Volume data is 93% full"); got == "Volume data is 93% full" {
		t.Errorf("German not applied: %q", got)
	}
}
