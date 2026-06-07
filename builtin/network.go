// network.go — NetworkDriver scaffold for FusionCompute. Networks map
// to FusionCompute "Distributed Virtual Switch (DVS) portgroups" —
// the VRM provisions them across every CNA in the cluster and binds
// per-VM vNICs to them.

package builtin

import (
	"context"
	"log/slog"

	drivers "github.com/openweft/weft-drivers"
)

type dcsNetwork struct {
	opts Options
	log  *slog.Logger
}

func newDCSNetwork(opts Options) *dcsNetwork {
	return &dcsNetwork{opts: opts, log: opts.Logger}
}

func (n *dcsNetwork) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{
		UUID:     "dcs-" + n.opts.SiteUUID,
		Hostname: n.opts.VRMEndpoint,
	}, nil
}

// EnsureNetwork POSTs /service/sites/{site}/dvswitches/{dvs}/portgroups
// with the VLAN id / IP pool / QoS hints. Idempotent : 409 from the
// VRM (existing portgroup) is collapsed to nil here.
func (n *dcsNetwork) EnsureNetwork(ctx context.Context, spec drivers.NetworkSpec) error {
	return drivers.ErrUnsupported
}

func (n *dcsNetwork) DestroyNetwork(ctx context.Context, networkUUID string) error {
	return drivers.ErrUnsupported
}

// AttachPort allocates a vNIC on the target portgroup and returns its
// device UUID as the NICHandle. The actual binding to a VM happens at
// dcsHypervisor.AttachNIC time.
func (n *dcsNetwork) AttachPort(ctx context.Context, spec drivers.PortSpec) (drivers.NICHandle, error) {
	return drivers.NICHandle{}, drivers.ErrUnsupported
}

func (n *dcsNetwork) DetachPort(ctx context.Context, portUUID string) error {
	return drivers.ErrUnsupported
}

// RotateMeshPeer : the WireGuard mesh runs as overlay microVMs on top
// of FusionCompute VMs, not at the DVS layer. ErrNotApplicable surfaces
// the design boundary explicitly.
func (n *dcsNetwork) RotateMeshPeer(ctx context.Context, spec drivers.PortSpec) error {
	return drivers.ErrNotApplicable
}
