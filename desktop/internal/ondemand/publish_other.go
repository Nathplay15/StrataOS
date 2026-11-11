//go:build !darwin

package ondemand

// PublishAgent tells the macOS extensions where the agent listens (macOS only).
func PublishAgent(port int, token string, folders []string) error { return nil }
