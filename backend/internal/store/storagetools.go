package store

import "time"

// BurnInStep is one stage of a disk burn-in test.
type BurnInStep struct {
	Name   string `json:"name"`   // smart-short | badblocks | smart-long
	Status string `json:"status"` // passed | failed | skipped | cancelled
	Detail string `json:"detail,omitempty"`
}

// BurnInAttr compares a SMART counter before and after the test.
type BurnInAttr struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Before int    `json:"before"`
	After  int    `json:"after"`
}

// BurnInReport is the result of a burn-in test, kept per disk serial number
// (kernel names change between boots).
type BurnInReport struct {
	ID        string       `json:"id"`
	Device    string       `json:"device"`
	Serial    string       `json:"serial"`
	Model     string       `json:"model"`
	Size      int64        `json:"size"`
	Mode      string       `json:"mode"` // read (non-destructive) | write (erases the disk)
	Started   time.Time    `json:"started"`
	Finished  time.Time    `json:"finished"`
	Result    string       `json:"result"` // passed | failed | cancelled
	Problems  []string     `json:"problems,omitempty"`
	Warnings  []string     `json:"warnings,omitempty"`
	BadBlocks int64        `json:"badBlocks"`
	Steps     []BurnInStep `json:"steps"`
	Attrs     []BurnInAttr `json:"attrs,omitempty"`
	// Blocks > 0: only the first Blocks blocks of 4 KiB were tested (test runs).
	Blocks int64  `json:"blocks,omitempty"`
	User   string `json:"user,omitempty"`
}

// MaxBurnIns bounds the reports kept in the state file.
const MaxBurnIns = 50

// FanPoint is a point of a fan curve: at Temp °C, fans run at PWM percent.
type FanPoint struct {
	Temp int `json:"temp"`
	PWM  int `json:"pwm"`
}

// Hardware holds the fan profile and the front-panel LED settings.
type Hardware struct {
	// FanProfile: "" or "bios" (the firmware drives the fans), quiet, balanced, cool, custom.
	FanProfile string     `json:"fanProfile,omitempty"`
	CPUCurve   []FanPoint `json:"cpuCurve,omitempty"`  // custom profile
	DiskCurve  []FanPoint `json:"diskCurve,omitempty"` // custom profile
	// LEDs maps an LED name of /sys/class/leds to on | off | disk ("" or absent: untouched).
	LEDs map[string]string `json:"leds,omitempty"`
}
