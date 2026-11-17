package api

import "testing"

func TestModelNameRe(t *testing.T) {
	for name, want := range map[string]bool{
		"mistral:7b":                  true,
		"llama3.2:3b-instruct-q4_K_M": true,
		"hf.co/mistralai/Mistral-7B":  true,
		"my-gguf":                     true,
		"":                            false,
		"../etc/passwd":               false,
		"model\nname":                 false,
		"model name":                  false,
		"-leading":                    false,
	} {
		if modelNameRe.MatchString(name) != want {
			t.Errorf("%q: want %v", name, want)
		}
	}
}
