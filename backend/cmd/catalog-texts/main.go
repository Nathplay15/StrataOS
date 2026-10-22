// Command catalog-texts writes frontend/src/i18n/catalog.ts, the list of the
// App Center catalog texts to translate:
//
//	go run ./cmd/catalog-texts [--check] ../frontend/src/i18n/catalog.ts
//
// With --check it only fails when the file is out of date (CI).
package main

import (
	"fmt"
	"os"

	"strataos/internal/docker"
)

func main() {
	check, target := false, ""
	for _, a := range os.Args[1:] {
		if a == "--check" {
			check = true
		} else {
			target = a
		}
	}
	text := docker.CatalogTS()
	switch {
	case target == "":
		fmt.Print(text)
	case check:
		if cur, err := os.ReadFile(target); err != nil || string(cur) != text {
			fmt.Fprintf(os.Stderr, "%s is out of date: run make catalog-texts\n", target)
			os.Exit(1)
		}
	default:
		if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
