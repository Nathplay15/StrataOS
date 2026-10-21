package hardware

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// HwmonRoot is where the kernel publishes the sensors (changed by tests).
var HwmonRoot = "/sys/class/hwmon"

// PWM is a fan output of a hwmon chip.
type PWM struct {
	ID     string `json:"id"` // hwmonN/pwmK (hwmon numbers may change at boot: never stored)
	Chip   string `json:"chip"`
	Label  string `json:"label"`
	Value  int    `json:"value"`  // 0..255
	Enable int    `json:"enable"` // pwmK_enable: 0 full speed, 1 manual, 2+ automatic (chip specific)
	RPM    int    `json:"rpm"`    // matching fanK_input, -1 when absent
	// Controllable: the output and its mode can be written.
	Controllable bool `json:"controllable"`
	path         string
}

// FanSensor is a tachometer without a usable PWM output.
type FanSensor struct {
	Chip  string `json:"chip"`
	Label string `json:"label"`
	RPM   int    `json:"rpm"`
}

var pwmRe = regexp.MustCompile(`^pwm(\d+)$`)
var fanRe = regexp.MustCompile(`^fan(\d+)_input$`)

func readInt(p string) (int, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func readStr(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func writable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().Perm()&0o200 != 0
}

// Scan lists the PWM outputs and the fan tachometers of every hwmon chip.
func Scan() (pwms []PWM, sensors []FanSensor) {
	dirs, _ := filepath.Glob(filepath.Join(HwmonRoot, "hwmon*"))
	sort.Strings(dirs)
	for _, d := range dirs {
		chip := readStr(filepath.Join(d, "name"))
		entries, _ := os.ReadDir(d)
		used := map[string]bool{}
		for _, e := range entries {
			m := pwmRe.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			p := filepath.Join(d, e.Name())
			v, err := readInt(p)
			if err != nil {
				continue
			}
			en, enErr := readInt(p + "_enable")
			x := PWM{ID: filepath.Base(d) + "/" + e.Name(), Chip: chip, Label: "Fan " + m[1], Value: v, Enable: en, RPM: -1, path: p}
			x.Controllable = enErr == nil && writable(p) && writable(p+"_enable")
			if rpm, err := readInt(filepath.Join(d, "fan"+m[1]+"_input")); err == nil {
				x.RPM = rpm
				used["fan"+m[1]+"_input"] = true
			}
			if l := readStr(filepath.Join(d, "fan"+m[1]+"_label")); l != "" {
				x.Label = l
			}
			pwms = append(pwms, x)
		}
		for _, e := range entries {
			m := fanRe.FindStringSubmatch(e.Name())
			if m == nil || used[e.Name()] {
				continue
			}
			rpm, err := readInt(filepath.Join(d, e.Name()))
			if err != nil {
				continue
			}
			label := readStr(filepath.Join(d, "fan"+m[1]+"_label"))
			if label == "" {
				label = "Fan " + m[1]
			}
			sensors = append(sensors, FanSensor{Chip: chip, Label: label, RPM: rpm})
		}
	}
	return
}

// cpuChips are the hwmon drivers of processor temperature sensors.
var cpuChips = map[string]bool{"coretemp": true, "k10temp": true, "zenpower": true, "cpu_thermal": true, "soc_thermal": true}

// CPUTemp returns the hottest processor sensor (NaN when none is readable).
func CPUTemp() float64 {
	best := math.NaN()
	dirs, _ := filepath.Glob(filepath.Join(HwmonRoot, "hwmon*"))
	for _, d := range dirs {
		if !cpuChips[readStr(filepath.Join(d, "name"))] {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		for _, in := range inputs {
			v, err := readInt(in)
			if err != nil || v <= 0 || v > 150000 {
				continue
			}
			if t := float64(v) / 1000; math.IsNaN(best) || t > best {
				best = t
			}
		}
	}
	return best
}

// original is the state of an output before StrataOS took it over.
type original struct {
	Enable int `json:"enable"`
	Value  int `json:"value"`
}

// Controller drives the PWM outputs. The original modes are saved to a file
// before the first write, so that they are restored even after a crash.
type Controller struct {
	mu        sync.Mutex
	stateFile string
	orig      map[string]original // pwm path → original
	hyst      Hysteresis
	// Last applied speed, and whether it is the full-speed fallback.
	Percent  float64
	Fallback bool
	LastErr  string
}

// NewController loads the originals left by a previous run.
func NewController(stateFile string) *Controller {
	c := &Controller{stateFile: stateFile, orig: map[string]original{}, hyst: Hysteresis{Degrees: 3}}
	if b, err := os.ReadFile(stateFile); err == nil {
		_ = json.Unmarshal(b, &c.orig)
	}
	return c
}

// State returns the last applied speed, whether it is the full-speed
// fallback (no temperature readable) and the last write error.
func (c *Controller) State() (pct float64, fallback bool, lastErr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Percent, c.Fallback, c.LastErr
}

// Active reports whether StrataOS currently drives fans.
func (c *Controller) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.orig) > 0
}

func (c *Controller) saveLocked() error {
	if len(c.orig) == 0 {
		err := os.Remove(c.stateFile)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	b, _ := json.Marshal(c.orig)
	return os.WriteFile(c.stateFile, b, 0o600)
}

func writeInt(p string, v int) error {
	return os.WriteFile(p, []byte(strconv.Itoa(v)), 0o644)
}

// Drive sets every controllable output to pct percent, taking manual control first.
func (c *Controller) Drive(pwms []PWM, pct float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []string
	for _, p := range pwms {
		if !p.Controllable {
			continue
		}
		if _, ok := c.orig[p.path]; !ok {
			c.orig[p.path] = original{Enable: p.Enable, Value: p.Value}
			if err := c.saveLocked(); err != nil {
				delete(c.orig, p.path)
				errs = append(errs, err.Error())
				continue
			}
		}
		if p.Enable != 1 {
			if err := writeInt(p.path+"_enable", 1); err != nil {
				errs = append(errs, fmt.Sprintf("%s: manual mode refused: %v", p.ID, err))
				continue
			}
		}
		if err := writeInt(p.path, int(math.Round(pct*255/100))); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p.ID, err))
		}
	}
	c.Percent = pct
	if len(errs) > 0 {
		c.LastErr = strings.Join(errs, "; ")
		return fmt.Errorf("%s", c.LastErr)
	}
	c.LastErr = ""
	return nil
}

// Step computes the speed for a reading (hysteresis, full-speed fallback) and applies it.
func (c *Controller) Step(pwms []PWM, cpuCurve, diskCurve []pt, t Temps) error {
	c.mu.Lock()
	pct, ok := c.hyst.Step(cpuCurve, diskCurve, t)
	c.Fallback = !ok
	c.mu.Unlock()
	return c.Drive(pwms, pct)
}

// Release gives the outputs back to the firmware with their original mode.
func (c *Controller) Release() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []string
	for p, o := range c.orig {
		if o.Enable == 1 {
			// Already manual before StrataOS: put the old speed back.
			_ = writeInt(p, o.Value)
		}
		if err := writeInt(p+"_enable", o.Enable); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err.Error())
			// Never leave a fan slow without anyone driving it.
			_ = writeInt(p, 255)
		}
		delete(c.orig, p)
	}
	c.hyst.Reset()
	c.Percent, c.Fallback = 0, false
	_ = c.saveLocked()
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
