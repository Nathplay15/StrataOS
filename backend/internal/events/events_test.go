package events

import "testing"

func TestPublish(t *testing.T) {
	Reset()
	defer Reset()
	var got []Event
	Subscribe(Components, func(e Event) { got = append(got, e) })
	Subscribe(Components, func(Event) { panic("boom") })
	Subscribe(Components, func(e Event) { got = append(got, e) })
	Publish(Disks, "")
	Publish(Components, "media")
	if len(got) != 2 || got[0].Topic != "media" {
		t.Fatalf("got %v", got)
	}
}
