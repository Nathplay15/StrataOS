package i18n

import (
	"os/exec"
	"strings"
)

// systemLang reads the first preferred language of macOS ("fr-FR").
func systemLang() string {
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.Trim(strings.TrimSpace(l), `(),"`)
		if l != "" {
			return l
		}
	}
	return ""
}
