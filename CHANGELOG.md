# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Operational REST client for the FusionCompute VRM.**
  `builtin/vrm_client.go` implements the cookie-based session auth
  (`POST /service/session` returning `X-Auth-Token`), the SHA-256
  password hashing the VRM expects, transparent token refresh on
  401 with bounded retry, and `waitTask` to poll
  `/service/tasks/{uuid}` until terminal status. TLS-skip-verify is
  operator opt-in via `Insecure` ; idle-connection pooling
  configured for a typical 5-VM cluster.
- **HypervisorDriver fully wired against the REST API** :
  `CreateVM` POSTs `/service/sites/{site}/vms` with osOptions /
  vmConfig / location subobjects, awaits the task ; `StartVM` /
  `StopVM` hit `action/start` and `action/stop` (graceful "safe"
  mode, escalating to "force" on ctx deadline) ; `DeleteVM`
  DELETEs with `isFormat=true` so attached disks are reclaimed in
  the same op ; `AttachDisk` / `DetachDisk` / `AttachNIC` /
  `DetachNIC` post the matching action endpoints with URN-formed
  volume + portgroup references.
- **VolumeDriver against /service/sites/{site}/volumes** :
  `EnsureVolume` POSTs the new volume with thin provisioning
  (operator can flip to thick via future input) ; `DestroyVolume`
  DELETEs ; both idempotent (409 → already exists, 404 → already
  gone). `AttachVolume` returns the VRM URN as `BackingPath` so
  `HypervisorDriver.AttachDisk` consumes the same handle.
- **6 unit tests** against an `httptest.Server` covering : login +
  token caching, 401 → refresh, task failure surfaces the VRM's
  `reason` string, 404 → `isNotFound`, the string-match helpers.
- Cross-builds : darwin/arm64 (vet), linux/amd64 (production target
  for Huawei x86 hosts), linux/arm64 (Huawei Kunpeng / Ascend).

### Status

The fundamentals of the FusionCompute REST integration ship. Three
operations still return `drivers.ErrUnsupported` by design :

- **Network plane** : portgroup CRUD via the DVS endpoints is
  next, but the mesh / overlay layer that connects tenant traffic
  lives in `weft-network` (not at this driver layer).
- **Image management** : OCI → FusionCompute template via the
  `/templates` OVF-import path is the next milestone ; the v0.1
  workflow expects pre-loaded templates referenced by URN.
- **Snapshot / backup ops on VolumeDriver** route through
  `weft-block` for cluster-replicated semantics that survive a DC
  outage. FusionCompute has its own snapshot service but its
  retention guarantees don't match what `weft-block` delivers ; we
  keep the routing decision explicit by stubbing here.

## [Earlier scaffold release] — 2026-06 (superseded above)

### Added

- Initial scaffold for the Huawei FusionCompute / UVP hypervisor
  driver. Fourth backend alongside `weft-driver-vz` (macOS),
  `weft-driver-qemu` (Linux), `weft-driver-vmd` (OpenBSD). FusionCompute
  is the enterprise-virtualization platform around Huawei's UVP
  hypervisor — central VRM (Virtualization Resource Manager) +
  per-host CNAs (Compute Node Agents).
- Plugin handshake + go-plugin gRPC server in place ; the four driver
  interfaces (Hypervisor, Volume, Network, Image) are wired and return
  `drivers.ErrUnsupported` from every method until the FusionCompute
  REST integration ships. The driver registers cleanly through
  go-plugin so an operator can wire it into `cluster.hcl` and verify
  discovery.
- `builtin/bundle.go` : `Options` (VRMEndpoint, Username, Password,
  SiteUUID, ClusterUUID, DatastoreUUID, PortgroupUUID, Insecure,
  Logger) + `NewBundle` constructor.
- `builtin/dcs.go` : `dcsHypervisor` skeleton, REST endpoint paths
  commented next to each VM-lifecycle method
  (`POST /service/sites/{site}/vms`, `action/start`, `action/stop`,
  `action/attachvol`, `action/attachnic`, etc.).
- `builtin/volume.go` : `dcsVolume` for cluster-wide datastores
  (`Local()` returns false ; AttachVolume/DetachVolume delegate to
  the per-VM action wrappers once wired).
- `builtin/network.go` : `dcsNetwork` for FusionCompute DVS portgroups.
  `RotateMeshPeer` returns `ErrNotApplicable` (mesh peers run as
  overlay microVMs, not at the DVS layer).
- `builtin/image.go` : `dcsImage` for the FusionCompute Image
  Management service (OCI → template via `/templates`).
- `cmd/weft-driver-dcs/main.go` : plugin entrypoint, reads VRM
  credentials from env (`WEFT_DCS_VRM_ENDPOINT`, `WEFT_DCS_USERNAME`,
  `WEFT_DCS_PASSWORD`, …).
- `README.md` documents the dual-transport boundary :
  - weft ↔ driver = **gRPC only** (via go-plugin handshake)
  - driver ↔ FusionCompute = **REST** (the only API the VRM exposes)

### Important : transport

The driver speaks **gRPC only** with weft-agent (standard openweft
go-plugin contract over stdin/stdout). It speaks **REST** with the
Huawei VRM because that's the API FusionCompute exposes ; there is no
alternative on Huawei's side. The two boundaries are independent —
operators only see the gRPC side.

### Status

This is a **scaffold release**. Functional VM/volume/network/image
operations are the next milestones (rough order in the README).
