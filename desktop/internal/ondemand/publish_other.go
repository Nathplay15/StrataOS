//go:build !darwin

package ondemand

// PublishAgent tells the macOS extensions where the agent listens (macOS only).
func PublishAgent(port int, token string, folders []string) error { return nil }

// RemoveDomain removes the File Provider domain of an account (macOS only).
func RemoveDomain(account string) error { return nil }
