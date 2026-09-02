package service

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// ListenReporter is what AdminNetworkService needs to know about the hub's
// live listeners. The hub's listener set implements it.
//
// An interface rather than the concrete type because this package sits BELOW
// the hub package that owns the listeners: hub imports service, so service
// cannot import hub. It is also what lets a test state a listener set without
// binding a socket.
type ListenReporter interface {
	// Bound reports every address the hub serves on, plus every configured
	// address it could not bind.
	Bound() []BoundListenAddress
	// PrimaryListenAddr is the address a browser-facing URL should name, in
	// the form -listen carries, or "" when the hub binds no TCP address.
	PrimaryListenAddr() string
	// HasNonLoopbackAddress reports whether the hub answers on an address
	// another machine can reach.
	HasNonLoopbackAddress() bool
}

// BoundListenAddress is one entry of the listener report. It mirrors the hub's
// own type, and the hub converts: a shared struct would have to live in a
// third package that neither side owns.
type BoundListenAddress struct {
	Address string
	Source  string
	Err     string
}

// AdminNetworkService reports which addresses the hub answers on.
//
// READ ONLY, deliberately. Writing the address list goes through
// AdminSettingsService like every other setting, so the list has one home, one
// validator, one audit trail and one CLI verb. This service exists for the
// facts a settings row cannot carry: what interfaces this machine has, and
// what the hub's sockets actually hold right now.
type AdminNetworkService struct {
	cfg      *config.Config
	set      *settings.Manager
	soloGate *auth.SoloGate
	listen   ListenReporter
	// interfaces is the machine-interface source, injectable so a test can
	// state a machine rather than assert about the one it runs on.
	interfaces func() ([]net.Interface, error)
}

// NewAdminNetworkService builds the service over the hub's listener set.
func NewAdminNetworkService(cfg *config.Config, set *settings.Manager, gate *auth.SoloGate, listen ListenReporter) *AdminNetworkService {
	return &AdminNetworkService{cfg: cfg, set: set, soloGate: gate, listen: listen, interfaces: net.Interfaces}
}

// GetListenStatus reports the machine's interfaces and the hub's live
// listeners.
func (s *AdminNetworkService) GetListenStatus(
	ctx context.Context, _ *connect.Request[leapmuxv1.GetListenStatusRequest],
) (*connect.Response[leapmuxv1.GetListenStatusResponse], error) {
	ifaces, err := s.machineInterfaces()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("read network interfaces: %w", err))
	}

	// The stored list, read back through the same key the panel writes, so
	// the panel never has to trust that its own write landed.
	stored := settings.KeyExtraListenAddresses.Of(s.set.Snapshot(ctx))
	configured := make([]string, 0, len(stored.Addresses))
	for _, raw := range stored.Addresses {
		// Canonically, so the picker matches a stored entry against the
		// address it offers whichever spelling the row holds.
		if addr, parseErr := listenset.Parse(raw); parseErr == nil {
			configured = append(configured, addr.String())
			continue
		}
		// An unparseable entry is shown as written. The validator refuses
		// one, so this is a row edited outside the hub, and hiding it would
		// leave an operator unable to see what to correct.
		configured = append(configured, raw)
	}

	out := &leapmuxv1.GetListenStatusResponse{
		Interfaces:     ifaces,
		DefaultAddress: s.cfg.Listen,
		Configured:     configured,
		PasswordSet:    s.soloGate.PasswordSet(ctx),
	}
	if s.listen != nil {
		for _, b := range s.listen.Bound() {
			out.Bound = append(out.Bound, &leapmuxv1.BoundAddress{
				Address: b.Address,
				Source:  b.Source,
				Error:   b.Err,
			})
		}
	}
	return connect.NewResponse(out), nil
}

// machineInterfaces lists the interfaces an address can be bound to.
//
// An interface that is DOWN is still listed, with up=false. Its addresses
// cannot be bound now, and a panel that hid them would silently drop the
// address an operator had already configured on a VPN that is not connected
// yet -- so the panel shows them and says which are usable.
//
// An interface with no addresses is omitted: there is nothing to offer, and a
// name with an empty list only adds noise to the picker.
func (s *AdminNetworkService) machineInterfaces() ([]*leapmuxv1.NetworkInterface, error) {
	ifaces, err := s.interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]*leapmuxv1.NetworkInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			// One unreadable interface must not withhold the rest: the
			// picker's job is to offer what it can see.
			continue
		}
		entry := &leapmuxv1.NetworkInterface{
			Name: iface.Name,
			Up:   iface.Flags&net.FlagUp != 0,
		}
		for _, a := range addrs {
			if converted, ok := interfaceAddress(a, iface.Name); ok {
				entry.Addresses = append(entry.Addresses, converted)
			}
		}
		if len(entry.Addresses) == 0 {
			continue
		}
		out = append(out, entry)
	}
	// Sorted, so two reads of an unchanged machine are equal and the picker
	// does not reorder itself between openings.
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

// interfaceAddress converts one interface address, and reports whether it is
// one the hub could bind.
//
// A LINK-LOCAL IPv6 address takes the interface's zone. Without it the address
// is ambiguous -- fe80::/10 repeats on every interface -- and net.Listen
// refuses it, so offering the bare literal in the picker would produce an
// entry that always fails to bind.
func interfaceAddress(a net.Addr, ifaceName string) (*leapmuxv1.NetworkInterfaceAddress, bool) {
	ipNet, ok := a.(*net.IPNet)
	if !ok {
		return nil, false
	}
	addr, ok := netip.AddrFromSlice(ipNet.IP)
	if !ok {
		return nil, false
	}
	addr = addr.Unmap()
	if addr.IsLinkLocalUnicast() && addr.Is6() {
		addr = addr.WithZone(ifaceName)
	}
	return &leapmuxv1.NetworkInterfaceAddress{
		Ip:       addr.String(),
		Ipv6:     addr.Is6(),
		Loopback: addr.IsLoopback(),
	}, true
}

// SetInterfaceSourceForTest replaces the machine-interface source.
//
// A seam, because no fixture can make net.Interfaces FAIL: the failure path
// reports an internal error, and reporting an empty machine instead would make
// the picker offer only the wildcard and look like a host with no network.
func SetInterfaceSourceForTest(s *AdminNetworkService, src func() ([]net.Interface, error)) {
	s.interfaces = src
}
