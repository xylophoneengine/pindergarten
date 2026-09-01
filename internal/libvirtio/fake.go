package libvirtio

import (
	"fmt"
	"regexp"
	"sort"
)

var fakeNameRe = regexp.MustCompile(`<name>(.*?)</name>`)

// Fake is an in-memory Hypervisor for tests.
type Fake struct {
	ConnURI   string
	XML       map[string]string   // name -> domain xml
	States    map[string]DomState // default StateShutoff
	RO        bool
	ROReason  string
	DefineErr error
	Defined   []string // xml passed to Define, in order
}

var _ Hypervisor = (*Fake)(nil)

func (f *Fake) URI() string { return f.ConnURI }

func (f *Fake) ReadOnly() (bool, string) { return f.RO, f.ROReason }

func (f *Fake) ListDomains() ([]Domain, error) {
	names := make([]string, 0, len(f.XML))
	for name := range f.XML {
		names = append(names, name)
	}
	sort.Strings(names)

	doms := make([]Domain, 0, len(names))
	for _, name := range names {
		xml := f.XML[name]
		state, ok := f.States[name]
		if !ok {
			state = StateShutoff
		}

		cfg, err := ParseDomainXML(xml)
		if err != nil {
			cfg = fallbackConfig(regexpName(xml, name), xml)
		}

		doms = append(doms, Domain{Config: cfg, State: state, ParseErr: err})
	}
	return doms, nil
}

func (f *Fake) DomainXML(name string) (string, error) {
	xml, ok := f.XML[name]
	if !ok {
		return "", fmt.Errorf("libvirtio: fake: domain %q not found", name)
	}
	return xml, nil
}

func (f *Fake) Define(xml string) error {
	f.Defined = append(f.Defined, xml)
	if f.DefineErr != nil {
		return f.DefineErr
	}

	name := ""
	if cfg, err := ParseDomainXML(xml); err == nil {
		name = cfg.Name
	} else {
		name = regexpName(xml, "")
	}
	if name == "" {
		return fmt.Errorf("libvirtio: fake: define: could not determine domain name")
	}
	if f.XML == nil {
		f.XML = map[string]string{}
	}
	f.XML[name] = xml
	return nil
}

func (f *Fake) Close() {}

// regexpName extracts a domain name from raw XML via a best-effort regexp
// match, falling back to fallback when no <name> element is found.
func regexpName(raw, fallback string) string {
	if m := fakeNameRe.FindStringSubmatch(raw); m != nil && m[1] != "" {
		return m[1]
	}
	return fallback
}
