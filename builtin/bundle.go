// Package builtin assembles the four driver interfaces a HostHandle expects
// (Hypervisor, Volume, Network, Image) into one Bundle the cmd entrypoint
// hands to the plugin gRPC server.
//
// Huawei FusionCompute / UVP backend. The driver speaks gRPC to weft-agent
// via the standard openweft go-plugin contract ; it speaks the Huawei
// FusionCompute REST API to the upstream VRM (Virtualization Resource
// Manager) — there is no alternative, that's the surface FusionCompute
// exposes. See the package README for status — most methods return
// drivers.ErrUnsupported today ; the integration drops in one REST
// endpoint at a time without rewiring the plumbing.
package builtin

import (
	"log/slog"

	drivers "github.com/openweft/weft-drivers"
)

// Bundle is the all-in-one driver bundle the plugin entrypoint serves.
type Bundle struct {
	Hypervisor drivers.HypervisorDriver
	Volume     drivers.VolumeDriver
	Network    drivers.NetworkDriver
	Image      drivers.ImageDriver
}

// Options configures one FusionCompute backend instance.
//
// FusionCompute groups compute hosts ("CNAs" — Compute Node Agents)
// under a central VRM (Virtualization Resource Manager). The driver
// talks to the VRM ; the VRM dispatches to whichever CNA hosts the
// VM. From weft's point of view, the driver is cluster-wide (Local()
// returns false on every interface).
type Options struct {
	// VRMEndpoint is the base URL of the FusionCompute VRM, typically
	// https://vrm.example.com:7443. The driver appends /service/...
	// paths to it for each REST call.
	VRMEndpoint string
	// Username is the FusionCompute administrator account (e.g.
	// `gandalf`). Created via the FusionCompute web UI ; the driver
	// requires the "VRM Administrator" role at minimum.
	Username string
	// Password authenticates against the VRM. The driver exchanges it
	// for a session token at the first call ; tokens are refreshed
	// when they expire (FusionCompute defaults to 1 h TTL).
	Password string
	// SiteUUID identifies the FusionCompute site the workloads target.
	// FusionCompute supports multiple sites under one VRM ; empty
	// defaults to the VRM's primary site.
	SiteUUID string
	// ClusterUUID is the default compute cluster workloads land in
	// when a VMSpec doesn't pin one.
	ClusterUUID string
	// DatastoreUUID is the default storage backend for new VM disks.
	// FusionCompute datastores are LUN-backed (FC, iSCSI, FCoE) or
	// IP-SAN-backed (NFS, CIFS).
	DatastoreUUID string
	// PortgroupUUID is the default vNIC binding for new VMs.
	PortgroupUUID string
	// Insecure skips TLS verification against the VRM. NEVER use in
	// production — operators behind a corporate PKI override this with
	// a proper CA bundle via the standard SSL_CERT_FILE env var instead.
	Insecure bool
	// Logger is injected by main.go so the driver shares the agent's
	// slog handler.
	Logger *slog.Logger
}

// NewBundle returns a Bundle wiring all four drivers against the same
// FusionCompute VRM. Construction is cheap ; the actual VRM login
// happens lazily on the first call so the plugin handshake completes
// even when the VRM is briefly unreachable.
func NewBundle(opts Options) (*Bundle, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	hv, err := newDCSHypervisor(opts)
	if err != nil {
		return nil, err
	}
	return &Bundle{
		Hypervisor: hv,
		Volume:     newDCSVolume(opts),
		Network:    newDCSNetwork(opts),
		Image:      newDCSImage(opts),
	}, nil
}
