package ondemand

func supported() (bool, string) { return true, "fileprovider" }

// StartProvider adds the File Provider domain of the account.
func StartProvider(s *Service, o ProviderOptions) (Provider, error) { return nil, ErrUnsupported }
