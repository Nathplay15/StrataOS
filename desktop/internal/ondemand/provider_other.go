//go:build !linux && !windows && !darwin

package ondemand

func supported() (bool, string) { return false, "" }

// StartProvider is not available on this system.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) { return nil, ErrUnsupported }
