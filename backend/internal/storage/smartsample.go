package storage

// SmartSample is the part of a SMART reading kept in the health history:
// the counters that tell a disk is getting worse, and the wear of SSDs.
// Nil fields are not reported by the disk.
type SmartSample struct {
	Reallocated   *int64 `json:"re,omitempty"`
	Pending       *int64 `json:"pe,omitempty"`
	Uncorrectable *int64 `json:"un,omitempty"`
	CRCErrors     *int64 `json:"crc,omitempty"`
	MediaErrors   *int64 `json:"me,omitempty"` // NVMe
	Temperature   *int64 `json:"tp,omitempty"`
	PowerOnHours  *int64 `json:"poh,omitempty"`
	// SSD: percentage of the rated endurance used, spare cells left (%),
	// and bytes written by the host.
	PercentUsed *int64 `json:"us,omitempty"`
	Spare       *int64 `json:"sp,omitempty"`
	Written     *int64 `json:"wr,omitempty"`
}

func p64(v int64) *int64 { return &v }

// Sample extracts the history values of a reading.
func (info *SmartInfo) Sample() SmartSample {
	var s SmartSample
	if info.Temperature > 0 {
		s.Temperature = p64(int64(info.Temperature))
	}
	if info.PowerOnHours > 0 {
		s.PowerOnHours = p64(int64(info.PowerOnHours))
	}
	if n := info.nvmeLog; n.present {
		s.MediaErrors = p64(int64(n.MediaErrors))
		s.PercentUsed = p64(int64(n.PercentageUsed))
		s.Spare = p64(int64(n.AvailableSpare))
		if n.DataUnitsWritten > 0 {
			s.Written = p64(n.DataUnitsWritten * 512000)
		}
		return s
	}
	for _, a := range info.Attributes {
		raw := int64(rawCount(a.Raw))
		switch a.ID {
		case 5:
			s.Reallocated = p64(raw)
		case 197:
			s.Pending = p64(raw)
		case 198:
			s.Uncorrectable = p64(raw)
		case 199:
			s.CRCErrors = p64(raw)
		case 241:
			// Total_LBAs_Written: 512-byte sectors on most SATA SSDs.
			if a.Name == "Total_LBAs_Written" && raw > 0 {
				s.Written = p64(raw * 512)
			}
		case 177, 202, 231, 233:
			// Wear_Leveling_Count, Percent_Lifetime_Remain, SSD_Life_Left,
			// Media_Wearout_Indicator: normalized value from 100 (new) down.
			if info.solidState && a.Value > 0 && a.Value <= 100 && s.PercentUsed == nil {
				s.PercentUsed = p64(int64(100 - a.Value))
			}
		}
	}
	return s
}
