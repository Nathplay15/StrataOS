package vms

import (
	"encoding/xml"
)

// StrataNS is the namespace of the StrataOS metadata stored in each domain
// (template flag, USB devices remembered by serial number). It lives in the
// libvirt definition so it follows the VM through clones and backups.
const StrataNS = "https://strataos.org/xmlns/vm/1.0"

type usbMeta struct {
	Vendor  string `xml:"vendor,attr"`
	Product string `xml:"product,attr"`
	Serial  string `xml:"serial,attr,omitempty"`
}

type strataMeta struct {
	XMLName  xml.Name  `xml:"vm"`
	Template string    `xml:"template,attr,omitempty"`
	Cloud    string    `xml:"cloud,attr,omitempty"` // cloud image the VM was created from
	USB      []usbMeta `xml:"usb"`
}

func (m strataMeta) empty() bool { return m.Template == "" && m.Cloud == "" && len(m.USB) == 0 }

// readMeta returns the StrataOS metadata of a domain (zero value when absent).
func readMeta(name string) strataMeta {
	var m strataMeta
	out, err := virsh("metadata", name, StrataNS, "--config")
	if err != nil {
		return m
	}
	_ = xml.Unmarshal([]byte(out), &m)
	return m
}

// writeMeta replaces the StrataOS metadata of a stopped or running domain
// (the persistent definition only).
func writeMeta(name string, m strataMeta) error {
	if m.empty() {
		// Removing metadata that is not there is not an error for us.
		_, _ = virsh("metadata", name, StrataNS, "--config", "--remove")
		return nil
	}
	m.XMLName = xml.Name{Local: "vm"}
	b, err := xml.Marshal(m)
	if err != nil {
		return err
	}
	_, err = virsh("metadata", name, StrataNS, "--key", "strata", "--set", string(b), "--config")
	return err
}

// IsTemplate reports whether a VM was converted to a template.
func IsTemplate(name string) bool { return readMeta(name).Template == "yes" }
