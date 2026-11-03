package api

import (
	"fmt"
	"testing"
)

func TestValidDesktopIcons(t *testing.T) {
	ok := []map[string][2]int{
		nil,
		{"overview": {0, 0}, "storage": {0, 1}, "custom-my_stack": {3, 2}, "onlyoffice": {199, 199}},
	}
	for _, m := range ok {
		if err := validDesktopIcons(m); err != nil {
			t.Errorf("%v: %v", m, err)
		}
	}
	big := map[string][2]int{}
	for i := 0; i <= maxDesktopIcons; i++ {
		big[fmt.Sprintf("a%d", i)] = [2]int{i % 100, i / 100}
	}
	bad := []map[string][2]int{
		{"overview": {0, 0}, "storage": {0, 0}}, // same cell
		{"overview": {-1, 0}},
		{"overview": {0, maxDesktopCell}},
		{"../x": {0, 0}},
		{"": {0, 0}},
		{"a b": {0, 0}},
		big,
	}
	for _, m := range bad {
		if len(m) < 10 {
			if err := validDesktopIcons(m); err == nil {
				t.Errorf("%v accepted", m)
			}
		} else if validDesktopIcons(m) == nil {
			t.Errorf("%d icons accepted", len(m))
		}
	}
}

func TestValidDesktopWidgets(t *testing.T) {
	for _, w := range [][]string{nil, {}, {"status", "resources", "storage", "apps"}, {"temps", "ups", "notifications"}} {
		if err := validDesktopWidgets(w); err != nil {
			t.Errorf("%v: %v", w, err)
		}
	}
	for _, w := range [][]string{{"status", "status"}, {"nope"}, {""}, {"Status"}} {
		if validDesktopWidgets(w) == nil {
			t.Errorf("%v accepted", w)
		}
	}
}

func TestValidAppearance(t *testing.T) {
	ok := [][2]string{{"", ""}, {"aurora", "#4f6bff"}, {"strata", ""}, {"", "#000000"}}
	for _, c := range ok {
		if err := validAppearance(c[0], c[1], false); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	if err := validAppearance("custom", "", true); err != nil {
		t.Error(err)
	}
	bad := [][2]string{{"custom", ""}, {"../x", ""}, {"", "red"}, {"", "#FFF"}, {"", "#4F6BFF"}, {"", "#12345g"}, {"", "#1234567"}}
	for _, c := range bad {
		if validAppearance(c[0], c[1], false) == nil {
			t.Errorf("%v accepted", c)
		}
	}
}
