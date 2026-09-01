package libvirtio

// DomState is a coarse libvirt domain run state.
type DomState int

const (
	StateRunning DomState = iota
	StateShutoff
	StateOther
)

// Domain is a listed libvirt domain paired with its parsed config.
type Domain struct {
	Config   *DomainConfig
	State    DomState
	ParseErr error // non-nil => unsupported, view-only
}

// Hypervisor abstracts a libvirt connection so callers can be tested
// against Fake without a running libvirtd.
type Hypervisor interface {
	URI() string
	ReadOnly() (bool, string) // true + reason when writes impossible
	ListDomains() ([]Domain, error)
	DomainXML(name string) (string, error) // inactive/config XML
	Define(xml string) error
	Close()
}

// fallbackConfig builds the DomainConfig used when a domain's XML can't be
// parsed. Invariant: a *DomainConfig from this package always has non-nil
// VCPUPins and populated Raw; when ParseErr != nil only Name and Raw are
// meaningful. Shared by Fake and the real libvirt Hypervisor so a
// view-only domain always has the same shape regardless of backend.
func fallbackConfig(name, xml string) *DomainConfig {
	return &DomainConfig{Name: name, Raw: xml, VCPUPins: map[int][]int{}}
}
