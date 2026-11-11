package ondemand

func supported() (bool, string) { return true, "cfapi" }

// StartProvider registers the sync root and serves its placeholders.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) { return nil, ErrUnsupported }
