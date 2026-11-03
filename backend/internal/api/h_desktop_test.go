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
