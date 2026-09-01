package libvirtio

import (
	"fmt"
	"sort"

	"libvirt.org/go/libvirt"
)

// Connect opens a libvirt connection to uri. If a read-write connection
// fails, it retries read-only; a successful read-only connection is
// returned with ReadOnly() reporting the original read-write error as the
// reason writes are impossible. If both fail, the returned error mentions
// both failures.
func Connect(uri string) (Hypervisor, error) {
	c, rwErr := libvirt.NewConnect(uri)
	if rwErr == nil {
		return &conn{c: c, uri: uri}, nil
	}

	roC, roErr := libvirt.NewConnectReadOnly(uri)
	if roErr != nil {
		return nil, fmt.Errorf("libvirtio: connect %q: read-write: %w; read-only: %v", uri, rwErr, roErr)
	}
	return &conn{c: roC, uri: uri, ro: true, roReason: rwErr.Error()}, nil
}

// conn is the real Hypervisor, backed by the official libvirt cgo bindings.
type conn struct {
	c        *libvirt.Connect
	uri      string
	ro       bool
	roReason string
}

var _ Hypervisor = (*conn)(nil)

func (c *conn) URI() string { return c.uri }

func (c *conn) ReadOnly() (bool, string) { return c.ro, c.roReason }

func (c *conn) ListDomains() ([]Domain, error) {
	all, err := c.c.ListAllDomains(0) // 0 = active + inactive
	if err != nil {
		return nil, fmt.Errorf("libvirtio: list domains: %w", err)
	}

	doms := make([]Domain, 0, len(all))
	for i := range all {
		d := &all[i]
		doms = append(doms, describeDomain(d))
		_ = d.Free()
	}

	sort.Slice(doms, func(i, j int) bool { return doms[i].Config.Name < doms[j].Config.Name })
	return doms, nil
}

// describeDomain reads name, inactive XML, and state from a live libvirt
// domain handle and builds a Domain. It never returns an error itself: any
// failure (GetName/GetXMLDesc/GetState/ParseDomainXML) lands in ParseErr
// with a shape-compatible fallback Config, so one bad domain never aborts
// the listing.
func describeDomain(d *libvirt.Domain) Domain {
	name, err := d.GetName()
	if err != nil {
		name = ""
	}

	state := StateOther
	if st, _, err := d.GetState(); err == nil {
		switch st {
		case libvirt.DOMAIN_RUNNING:
			state = StateRunning
		case libvirt.DOMAIN_SHUTOFF:
			state = StateShutoff
		}
	}

	xml, err := d.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return Domain{Config: fallbackConfig(name, ""), State: state, ParseErr: err}
	}

	cfg, err := ParseDomainXML(xml)
	if err != nil {
		cfg = fallbackConfig(name, xml)
	}
	return Domain{Config: cfg, State: state, ParseErr: err}
}

func (c *conn) DomainXML(name string) (string, error) {
	d, err := c.c.LookupDomainByName(name)
	if err != nil {
		return "", fmt.Errorf("libvirtio: lookup domain %q: %w", name, err)
	}
	defer func() { _ = d.Free() }()

	xml, err := d.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return "", fmt.Errorf("libvirtio: get xml for domain %q: %w", name, err)
	}
	return xml, nil
}

func (c *conn) Define(xml string) error {
	d, err := c.c.DomainDefineXML(xml)
	if err != nil {
		return fmt.Errorf("libvirtio: define domain: %w", err)
	}
	_ = d.Free()
	return nil
}

func (c *conn) Close() {
	_, _ = c.c.Close()
}
