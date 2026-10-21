package docker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// CustomApp turns a compose stack into an App Center entry ("My apps"). It
// lives next to the stack (.strata-custom.json), so deleting the stack
// removes the app.
type CustomApp struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`    // http(s) URL, optional
	WebPort     int    `json:"webPort,omitempty"` // host port of the web interface, 0: none
}

const customFile = ".strata-custom.json"

// Validate checks and normalises the metadata of a custom app.
func (c *CustomApp) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Description = strings.TrimSpace(c.Description)
	c.Icon = strings.TrimSpace(c.Icon)
	if c.Name == "" || len(c.Name) > 64 {
		return fmt.Errorf("the name must have 1 to 64 characters")
	}
	if len(c.Description) > 300 {
		return fmt.Errorf("the description is too long (300 characters at most)")
	}
	for _, r := range c.Name + c.Description {
		if unicode.IsControl(r) {
			return fmt.Errorf("the name and description must be on one line")
		}
	}
	if c.Icon != "" {
		u, err := url.Parse(c.Icon)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(c.Icon) > 512 || strings.ContainsAny(c.Icon, " \"'<>`") {
			return fmt.Errorf("the icon must be an http(s) address")
		}
	}
	if c.WebPort < 0 || c.WebPort > 65535 {
		return fmt.Errorf("the web port must be a port number (1 to 65535)")
	}
	return nil
}

// ReadCustom returns the custom app metadata of a stack, nil when it is not one.
func ReadCustom(stack string) *CustomApp {
	if ValidStack(stack) != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(StacksDir, stack, customFile))
	if err != nil {
		return nil
	}
	var c CustomApp
	if json.Unmarshal(b, &c) != nil || c.Validate() != nil {
		return nil
	}
	return &c
}

// WriteCustom makes an existing stack a custom app (or updates it).
func WriteCustom(stack string, c CustomApp) error {
	if err := ValidStack(stack); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	dir := filepath.Join(StacksDir, stack)
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
		return fmt.Errorf("stack %s not found", stack)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".strata-app")); err == nil && strings.TrimSpace(string(b)) != "" {
		return fmt.Errorf("%s is already an App Center app", stack)
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := filepath.Join(dir, customFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, customFile))
}

// RemoveCustom turns a custom app back into a plain stack.
func RemoveCustom(stack string) error {
	if err := ValidStack(stack); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(StacksDir, stack, customFile))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// AppWebPort returns the host port of the web interface of the app (catalog
// or custom) installed as a stack, "" when there is none.
func AppWebPort(stack string) string {
	if c := ReadCustom(stack); c != nil {
		if c.WebPort > 0 {
			return strconv.Itoa(c.WebPort)
		}
		return ""
	}
	if ValidStack(stack) != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(StacksDir, stack, ".strata-app"))
	if err != nil {
		return ""
	}
	app := FindApp(strings.TrimSpace(string(b)))
	if app == nil || app.WebPort == "" {
		return ""
	}
	p := EnvValue(stack, app.WebPort)
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return ""
	}
	return p
}
