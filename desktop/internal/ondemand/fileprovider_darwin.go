package ondemand

func supported() (bool, string) { return true, "fileprovider" }

// StartProvider adds the File Provider domain of the account.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) { return nil, ErrUnsupported }

// PublishAgent tells the macOS extensions where the agent listens.
func PublishAgent(port int, token string, folders []string) error { return nil }
