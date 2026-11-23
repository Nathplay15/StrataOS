package nvr

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"strataos/internal/sys"
)

// Optional local object classifier for the built-in motion engine (a "light
// local model"): when the strata-detect binary is installed, each motion
// event's snapshot is sent to it and the labels come back (person, vehicle,
// shadow…). Events whose labels are not a person or a vehicle (trees,
// shadows…) are marked falsePositive and do not trigger a notification. The
// binary reads the JPEG on stdin and prints one label per line on stdout.
//
// The functions are variables so tests can stub the binary.
var (
	// ClassifierInstalled reports whether the classifier binary is present.
	ClassifierInstalled = func() bool { return sys.Has("strata-detect") }
	// Classify runs the classifier on a JPEG picture and returns its labels.
	Classify = func(ctx context.Context, jpeg []byte) ([]string, error) {
		cmd := exec.CommandContext(ctx, "strata-detect")
		cmd.Stdin = bytes.NewReader(jpeg)
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		var labels []string
		for _, l := range strings.Fields(string(out)) {
			labels = append(labels, strings.ToLower(l))
		}
		return labels, nil
	}
)

// interestingLabels are worth a notification; anything else (shadow, tree,
// light change…) is a false positive.
var interestingLabels = map[string]bool{
	"person": true, "vehicle": true, "car": true, "truck": true, "bus": true,
	"motorcycle": true, "bicycle": true, "dog": true, "cat": true,
}

// ClassifyMotion labels one motion snapshot: (best label, falsePositive).
// A classifier failure keeps the previous behavior (event, no label, no
// filtering): label "" and falsePositive false.
func ClassifyMotion(ctx context.Context, jpeg []byte) (string, bool) {
	labels, err := Classify(ctx, jpeg)
	if err != nil || len(labels) == 0 {
		return "", false
	}
	fp := true
	for _, l := range labels {
		if interestingLabels[l] {
			fp = false
			break
		}
	}
	return labels[0], fp
}
