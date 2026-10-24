//go:build !windows && !darwin

package i18n

func systemLang() string { return "" }
