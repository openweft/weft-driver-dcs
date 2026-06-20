<p align="center"><img src="https://raw.githubusercontent.com/openweft/brand/main/social/openweft.png" alt="openweft" width="720"></p>

# weft-driver-dcs

**Huawei FusionCompute / UVP hypervisor driver for weft** — the fourth
backend alongside `weft-driver-vz`, `weft-driver-qemu`, and
`weft-driver-vmd` :

| Backend  | Host OS / Platform | Hypervisor                              |
| -------- | ------------------ | --------------------------------------- |
| vz       | macOS              | Apple Virtualization Framework          |
| qemu     | Linux              | QEMU / KVM                              |
| vmd      | OpenBSD            | vmd(8) / vmctl(8) / vm.conf             |
| **dcs**  | Huawei FusionCompute | UVP via VRM (Virtualization Resource Manager) |

## What it is

[Huawei FusionCompute](https://e.huawei.com/en/products/cloud-computing-dc/cloud-computing/fusioncompute)
(formerly FusionSphere) is Huawei's enterprise virtualization platform
built around their UVP (Unified Virtualization Platform) hypervisor.
Each compute host runs a **CNA (Compute Node Agent)** ; a central
**VRM (Virtualization Resource Manager)** orchestrates them. The
driver talks to the VRM ; the VRM dispatches to CNAs.

This driver exposes the standard weft `HypervisorDriver` /
`VolumeDriver` / `NetworkDriver` / `ImageDriver` interfaces through
the openweft go-plugin gRPC contract.

## Transport — gRPC only (with weft) ; REST (with FusionCompute)

There are TWO communication boundaries to keep straight :

| Boundary                | Transport                                                |
| ----------------------- | -------------------------------------------------------- |
| weft-agent ↔ this driver | **gRPC** via the standard go-plugin handshake (stdin/stdout multiplexed). No HTTP, no extra ports. |
| this driver ↔ FusionCompute VRM | **REST** over HTTPS to `/service/sites/...` — that's the API surface FusionCompute exposes ; there is no gRPC alternative on Huawei's side. |

The two are independent : an operator wiring this driver into `cluster.hcl`
only sees the gRPC handshake side ; the REST plumbing to the VRM is
the driver's internal concern.

## Status — operational (2026-06)

The REST integration ships :

- **VRM session auth** : cookie-based via `POST /service/session`,
  SHA-256-hashed password, transparent token refresh on 401,
  request retry with bounded attempts.
- **Task polling** : every mutating call returns a task UUID ; the
  client polls `/service/tasks/{uuid}` until terminal status,
  surfacing FusionCompute's `reason` field on failures.
- **HypervisorDriver** : `CreateVM` (POSTs `/vms` with osOptions /
  vmConfig / location), `StartVM` (`action/start`), `StopVM`
  (`action/stop` graceful → force on ctx deadline), `DeleteVM`
  (DELETE with `isFormat=true` so attached disks reclaim in the
  same op), `AttachDisk` / `DetachDisk` / `AttachNIC` /
  `DetachNIC` (URN-formed volume / portgroup references).
- **VolumeDriver** : `EnsureVolume` (POSTs `/volumes` with thin
  provisioning), `DestroyVolume` (DELETE), both idempotent (409
  → already exists, 404 → already gone). `AttachVolume` returns
  the VRM URN as `BackingPath`.
- **6 unit tests** against an `httptest.Server` exercise the
  client end-to-end.

Endpoints commented next to each method :

- **VMs** : `POST /service/sites/{site}/vms`, `.../vms/{vm}/action/{start,stop,attachvol,attachnic,detachvol,detachnic}`
- **Volumes** : `POST /service/sites/{site}/volumes`, `DELETE /volumes/{volume}`
- **Networks** (next milestone) : `POST /service/sites/{site}/dvswitches/{dvs}/portgroups`
- **Images / Templates** (next milestone) : `POST /service/sites/{site}/templates`

## What the driver is for

- Operators with existing FusionCompute clusters who want weft as the
  **unified control plane** across heterogeneous fleets (Huawei +
  Linux KVM + Apple Silicon + OpenBSD). Same openweft catalogue,
  same snapshots/backups CLI, same federation primitives.
- Greenfield deployments with **mandate to use Huawei** for
  procurement, regulatory, or sovereignty reasons (common in CN,
  Africa, parts of Europe).

The driver does NOT compete with FusionCompute's own scheduling /
DRS — those stay in the VRM. weft routes its microVM workloads
through the driver's `CreateVM` / `AttachDisk` / `AttachNIC` and lets
the VRM place the VM on its preferred CNA ; the driver returns the
FusionCompute-side handle.

## Layout

| Path                              | Purpose                                                  |
| --------------------------------- | -------------------------------------------------------- |
| `cmd/weft-driver-dcs/main.go`     | Plugin entrypoint ; serves the gRPC contract             |
| `builtin/bundle.go`               | Bundle assembling the four driver interfaces             |
| `builtin/dcs.go`                  | HypervisorDriver impl against FusionCompute REST (scaffold) |
| `builtin/volume.go`               | VolumeDriver impl (datastore-backed volumes, scaffold)   |
| `builtin/network.go`              | NetworkDriver impl (DVS portgroups, scaffold)            |
| `builtin/image.go`                | ImageDriver impl (FusionCompute templates, scaffold)     |

## Build + register

```sh
cd weft-driver-dcs
go build -o weft-driver-dcs ./cmd/weft-driver-dcs

# Register in cluster.hcl on a host that can reach the VRM :
#   drivers {
#     dcs = "ghcr.io/openweft/weft-driver-dcs:v0.1.0"
#   }
#
# Pass VRM credentials via env (the agent reads them and forwards to
# the plugin process at launch) :
#   WEFT_DCS_VRM_ENDPOINT=https://vrm.example.cn:7443
#   WEFT_DCS_USERNAME=gandalf
#   WEFT_DCS_PASSWORD=...
#   WEFT_DCS_SITE_UUID=...    # optional, defaults to VRM's primary site
```

## Next steps (rough order)

1. Wire the FusionCompute REST client (cookie-based auth via
   `POST /service/session/login`, refresh on 401).
2. Implement HypervisorDriver.CreateVM via the `vms` POST.
3. Implement StartVM / StopVM / DeleteVM with task-polling on the
   202 responses (the VRM returns a task UUID for every mutation).
4. Implement VolumeDriver against `/service/sites/{site}/volumes`.
5. Implement NetworkDriver against `/dvswitches/{dvs}/portgroups`.
6. Implement ImageDriver via the OVF-import flow on `/templates`.
7. Acceptance tests against a FusionCompute simulator (Huawei ships
   a "FusionCompute Simulator" image for partners).
