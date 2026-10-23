// release-info checks a release tag against VERSION and prints what the
// release workflow needs, as lines for $GITHUB_ENV:
//
//	go run ./cmd/release-info v0.8.0-beta.1 "$(cat ../VERSION)"
//	V=0.8.0~beta.1        Debian version (package)
//	FV=0.8.0-beta.1       version in tags and file names
//	PRERELEASE=true       a GitHub pre-release, published to "testing" only
//	SUITES=testing        APT suites of the release, comma-separated (the output is sourced by the shell)
package main

import (
	"fmt"
	"os"
	"strings"

	"strataos/internal/release"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: release-info <tag> <version>")
		os.Exit(2)
	}
	tag, version := os.Args[1], strings.TrimSpace(os.Args[2])
	if err := release.Valid(version); err != nil {
		fail(err.Error())
	}
	v, err := release.FromTag(tag)
	if err != nil {
		fail(err.Error())
	}
	if v != version {
		fail(fmt.Sprintf("tag %s does not match VERSION %s (expected tag %s)", tag, version, release.Tag(version)))
	}
	fmt.Printf("V=%s\nFV=%s\nPRERELEASE=%t\nSUITES=%s\n", v, release.FileVersion(v), release.Prerelease(v), strings.Join(release.Suites(v), ","))
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
