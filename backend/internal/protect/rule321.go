package protect

import "time"

// Media where a copy of the data lives.
const (
	MediaNAS    = "nas"    // a volume of this NAS (other disks, same box)
	MediaUSB    = "usb"    // an external disk plugged into the NAS
	MediaRemote = "remote" // another NAS or server (SSH, SFTP, vault, REST)
	MediaCloud  = "cloud"  // cloud storage (S3, B2, Drive…)
)

// Copy is a backup task seen by the 3-2-1 assessment.
type Copy struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // backup | cloud
	Media   string    `json:"media"`
	Offsite bool      `json:"offsite"`
	Healthy bool      `json:"healthy"` // enabled, last run succeeded, not overdue
	LastOK  time.Time `json:"lastOk,omitzero"`
	Why     string    `json:"why,omitempty"` // why it is not healthy
}

// Rule321 is where the NAS stands: 3 copies of the data (the original
// counts), on 2 different media, 1 of them off-site.
type Rule321 struct {
	Copies  int      `json:"copies"`
	Media   int      `json:"media"`
	Offsite int      `json:"offsite"`
	OK      bool     `json:"ok"`
	Missing []string `json:"missing"` // copies | media | offsite
	List    []Copy   `json:"list"`
}

// Assess counts the healthy copies. Snapshots are not copies: they live on
// the same disks as the data.
func Assess(copies []Copy) Rule321 {
	r := Rule321{Copies: 1, List: copies, Missing: []string{}}
	media := map[string]bool{MediaNAS: true}
	for _, c := range copies {
		if !c.Healthy {
			continue
		}
		r.Copies++
		media[c.Media] = true
		if c.Offsite {
			r.Offsite++
		}
	}
	r.Media = len(media)
	if r.List == nil {
		r.List = []Copy{}
	}
	if r.Copies < 3 {
		r.Missing = append(r.Missing, "copies")
	}
	if r.Media < 2 {
		r.Missing = append(r.Missing, "media")
	}
	if r.Offsite < 1 {
		r.Missing = append(r.Missing, "offsite")
	}
	r.OK = len(r.Missing) == 0
	return r
}

// MediaOf classifies a backup destination. usb tells whether a local
// destination sits on an external disk.
func MediaOf(destType string, usb bool) (media string, offsite bool) {
	switch destType {
	case "local":
		if usb {
			return MediaUSB, false
		}
		return MediaNAS, false
	case "s3", "cloud":
		return MediaCloud, true
	default: // ssh, sftp, rest, strataos
		return MediaRemote, true
	}
}

// Overdue reports whether a task with this interval has gone too long
// without a success (twice the interval plus a day, like the advisor).
func Overdue(lastOK time.Time, interval time.Duration, now time.Time) bool {
	if lastOK.IsZero() {
		return true
	}
	if interval <= 0 {
		interval = 30 * 24 * time.Hour // on demand: a month
	}
	return now.Sub(lastOK) > 2*interval+24*time.Hour
}
