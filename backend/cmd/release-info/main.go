// release-info checks a release tag against VERSION and prints what the
// release workflow needs, as lines for $GITHUB_ENV:
//
//	go run ./cmd/release-info v0.8.0-beta.1 "$(cat ../VERSION)"
//	V=0.8.0~beta.1        Debian version (package)
//	FV=0.8.0-beta.1       version in tags and file names
//	PRERELEASE=true       a GitHub pre-release, published to "testing" only
//	SUITES=testing        APT suites of the release, comma-separated (the output is sourced by the shell)
//
// The apps (release-android.yml, release-desktop.yml) pass their tag prefix:
//
//	go run ./cmd/release-info -prefix android- android-v0.12.1 "$(cat ../android/VERSION)"
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"strataos/internal/release"
)

func main() {
	prefix := flag.String("prefix", "", "tag prefix of an app (android-, desktop-)")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: release-info [-prefix android-|desktop-] <tag> <version>")
		os.Exit(2)
	}
	tag, version := flag.Arg(0), strings.TrimSpace(flag.Arg(1))
	if err := release.Valid(version); err != nil {
		fail(err.Error())
	}
	v, err := release.FromTag(tag)
	if *prefix != "" {
		v, err = release.FromAppTag(tag, *prefix)
	}
	if err != nil {
		fail(err.Error())
	}
	if v != version {
		fail(fmt.Sprintf("tag %s does not match VERSION %s (expected tag %s%s)", tag, version, *prefix, release.Tag(version)))
	}
	fmt.Printf("V=%s\nFV=%s\nPRERELEASE=%t\nSUITES=%s\n", v, release.FileVersion(v), release.Prerelease(v), strings.Join(release.Suites(v), ","))
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
