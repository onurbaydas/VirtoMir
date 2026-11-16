package virt

import (
	"bytes"
	"encoding/xml"
	"os"
	"regexp"

	"libvirt.org/go/libvirtxml"
)

// RestoreXML makes an earlier definition of a machine (a version of its configuration
// history) its definition again. Unlike DefineXML, which takes what a person typed, it
// takes what the machine once was:
//
//   - the machine keeps the name it has now. It may have been renamed since that version
//     was saved, and a definition with the old name would be refused, leaving every version
//     from before a rename impossible to go back to;
//   - disks of that version which are no longer on the host are reported: the definition is
//     restored as it was, but the machine does not start until they are back or taken out.
func (c *Client) RestoreXML(uuid, xmlDoc string) (DefineResult, error) {
	_, d, err := c.lookup(uuid)
	if err != nil {
		return DefineResult{}, err
	}
	doc, err := restoredDefinition(xmlDoc, d.Name)
	if err != nil {
		return DefineResult{}, err
	}
	res, err := c.DefineXML(uuid, doc)
	if err != nil {
		return res, err
	}
	var x libvirtxml.Domain
	if x.Unmarshal(doc) == nil {
		res.MissingDisks = missingDisks(&x, fileExists)
	}
	return res, nil
}

var domainNameRE = regexp.MustCompile(`<name>[^<]*</name>`)

// restoredDefinition is doc with the machine's name set to name. Only the name element is
// rewritten, as text: the rest stays byte for byte what libvirt once gave, so nothing
// libvirtxml does not know is lost on the way.
func restoredDefinition(doc, name string) (string, error) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(doc); err != nil {
		return "", invalidf("The saved version is not valid XML: %v", err)
	}
	if x.Name == name {
		return doc, nil
	}
	var esc bytes.Buffer
	if err := xml.EscapeText(&esc, []byte(name)); err != nil {
		return "", err
	}
	// libvirt writes <name> first; the check below catches a document where it is not.
	loc := domainNameRE.FindStringIndex(doc)
	if loc == nil {
		return "", invalidf("The saved version has no <name>, so it cannot be restored as %s.", name)
	}
	out := doc[:loc[0]] + "<name>" + esc.String() + "</name>" + doc[loc[1]:]
	var y libvirtxml.Domain
	if err := y.Unmarshal(out); err != nil || y.Name != name {
		return "", invalidf("The saved version is named %s and could not be restored as %s: restore it from the XML tab with the name changed.", x.Name, name)
	}
	return out, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// missingDisks lists the disks, CD images and floppies of a definition whose file or device
// is not on this host. Disks on the network or in a pool by name are not looked at.
func missingDisks(x *libvirtxml.Domain, exists func(string) bool) []string {
	var out []string
	if x.Devices == nil {
		return out
	}
	for _, d := range x.Devices.Disks {
		if d.Source == nil {
			continue
		}
		var path string
		switch {
		case d.Source.File != nil:
			path = d.Source.File.File
		case d.Source.Block != nil:
			path = d.Source.Block.Dev
		}
		if path != "" && !exists(path) {
			out = appendUnique(out, path)
		}
	}
	return out
}
