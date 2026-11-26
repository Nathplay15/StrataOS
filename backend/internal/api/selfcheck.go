package api

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"strataos/internal/jobs"
	"strataos/internal/sys"
)

// Post-update self-check. Before installing a new StrataOS package, the
// running version writes updateCheckFile (the version it comes from, the
// services running) and arms a timer (strata-update-check, in 10 minutes)
// that runs scripts/update-check. The new version, at its start, checks that
// its API and web interface answer and that those services still run, then
// removes the file and stops the timer. If it cannot (it does not start, or
// a check fails), the timer installs the previous package again; that
// version then finds the file marked ROLLEDBACK and tells the administrators.

var (
	updateCheckFile  = "/var/lib/strata/update-check.env"
	updateCheckDelay = 10 * time.Minute
	versionRe        = regexp.MustCompile(`^[0-9A-Za-z.~+:-]{1,64}$`)
	checkedServices  = []string{"smbd", "nfs-server", "docker", "libvirtd"}
	// checkRun runs systemctl and systemd-run (tests replace it).
	checkRun = sys.Run
)

func activeServices() []string {
	var out []string
	for _, u := range checkedServices {
		if o, _ := checkRun("systemctl", "is-active", u); strings.TrimSpace(o) == "active" {
			out = append(out, u)
		}
	}
	return out
}

// armUpdateCheck runs just before the package is installed.
func (s *Server) armUpdateCheck(j *jobs.Job, from, to string) {
	if !versionRe.MatchString(from) || !versionRe.MatchString(to) || from == to {
		return
	}
	body := "FROM=" + from + "\nTO=" + to + "\nSERVICES=" + strings.Join(activeServices(), ",") + "\n"
	if err := sys.WriteFileAtomic(updateCheckFile, []byte(body), 0o600); err != nil {
		j.Logf("Self-check after the update not armed: %v", err)
		return
	}
	_, _ = checkRun("systemctl", "stop", "strata-update-check.timer")
	if _, err := checkRun("systemd-run", "--unit=strata-update-check", "--collect", "--on-active="+updateCheckDelay.String(),
		"--timer-property=AccuracySec=15s", scriptsDir+"/update-check"); err != nil {
		j.Logf("Self-check after the update not armed: %v", err)
		os.Remove(updateCheckFile)
		return
	}
	j.Logf("Self-check armed: without an answer of StrataOS %s within %s, %s is installed again", to, updateCheckDelay, from)
}

// disarmUpdateCheck: the update did not happen (the package did not change).
func disarmUpdateCheck() {
	_, _ = checkRun("systemctl", "stop", "strata-update-check.timer")
	os.Remove(updateCheckFile)
}

func readUpdateCheck() map[string]string {
	b, err := os.ReadFile(updateCheckFile)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

// updateSelfCheck runs at the start of the daemon.
func (s *Server) updateSelfCheck(base string, wait time.Duration) {
	m := readUpdateCheck()
	if m == nil {
		return
	}
	if m["ROLLEDBACK"] != "" {
		disarmUpdateCheck()
		msg := "StrataOS " + m["TO"] + " did not pass its checks after the update: version " + m["FROM"] + " was installed again"
		if m["ROLLBACKFAILED"] != "" {
			msg = "StrataOS " + m["TO"] + " did not pass its checks after the update, and installing " + m["FROM"] + " again failed: see the system log"
		}
		s.store.Log("error", "system", "", msg)
		s.notify("error", "StrataOS update undone", msg)
		return
	}
	if m["TO"] != "" && m["TO"] != s.version && !strings.HasPrefix(m["TO"], s.version) {
		// Started before the update finished (or another version): the
		// timer decides.
		return
	}
	time.Sleep(wait)
	if problem := selfCheck(base, strings.Split(m["SERVICES"], ",")); problem != "" {
		slog.Error("post-update self-check failed", "problem", problem)
		s.store.Log("error", "system", "", "Self-check after the update failed: "+problem)
		// Do not wait for the timer.
		_, _ = checkRun("systemctl", "start", "--no-block", "strata-update-check.service")
		return
	}
	disarmUpdateCheck()
	s.store.Log("info", "system", "", "Update to StrataOS "+s.version+" checked: the interface and the services answer")
}

// selfBase is the address the daemon reaches itself at (its own HTTPS port).
func (s *Server) selfBase() string {
	port := "7443"
	if s.certs != nil && s.certs.HTTPSPort != "" {
		port = s.certs.HTTPSPort
	}
	return "https://127.0.0.1:" + port
}

// selfCheck returns what does not work ("" when everything does).
func selfCheck(base string, services []string) string {
	// Its own certificate, on the loopback.
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
	get := func(p string) (int, string, error) {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", base+p, nil)
		resp, err := c.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, string(b), nil
	}
	var last string
	for range 6 { // up to a minute: the service may still be starting
		last = ""
		if code, _, err := get("/api/setup"); err != nil || code != 200 {
			last = "the API does not answer"
		} else if code, body, err := get("/"); err != nil || code != 200 || !strings.Contains(body, `id="root"`) {
			last = "the web interface does not answer"
		} else {
			for _, u := range services {
				if u == "" {
					continue
				}
				if o, _ := checkRun("systemctl", "is-active", u); strings.TrimSpace(o) != "active" {
					last = "the service " + u + " is not running"
					break
				}
			}
		}
		if last == "" {
			return ""
		}
		time.Sleep(10 * time.Second)
	}
	return last
}
