// dcs.go — HypervisorDriver implementation backed by the Huawei
// FusionCompute REST API. Calls land at /service/sites/{site}/vms/...
// and the VRM dispatches to whichever CNA hosts the VM. Mutations
// return a task UUID we poll until completion.
//
// Wire format references : "FusionCompute V100R006C00 ToB API Reference"
// (Huawei official docs). The JSON shapes mirror what the VRM expects ;
// fields not used by weft are omitted from the payload (the VRM
// fills in cluster defaults).

package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	drivers "github.com/openweft/weft-drivers"
)

type dcsHypervisor struct {
	opts Options
	log  *slog.Logger

	mu   sync.Mutex
	vrm  *vrmClient
}

func newDCSHypervisor(opts Options) (*dcsHypervisor, error) {
	if opts.VRMEndpoint == "" {
		return nil, drivers.ErrNotApplicable
	}
	return &dcsHypervisor{opts: opts, log: opts.Logger}, nil
}

// client returns the lazily-built REST client. We don't dial at
// construction so the plugin handshake completes even when the VRM
// is briefly unreachable ; the first lifecycle call triggers login.
func (h *dcsHypervisor) client() *vrmClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vrm == nil {
		h.vrm = newVRMClient(h.opts.VRMEndpoint, h.opts.Username, h.opts.Password, h.opts.Insecure, h.log)
	}
	return h.vrm
}

// HostInfo identifies this driver. FusionCompute is cluster-wide via
// the VRM ; we surface a synthetic HostInfo per the dispatch
// convention from weft-drivers/hypervisor.go.
func (h *dcsHypervisor) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{
		UUID:     "dcs-" + h.opts.SiteUUID,
		Hostname: h.opts.VRMEndpoint,
	}, nil
}

// vmsPath builds the per-site VMs collection URL.
func (h *dcsHypervisor) vmsPath() string {
	return "/service/sites/" + h.opts.SiteUUID + "/vms"
}

func (h *dcsHypervisor) vmPath(vmUUID string) string {
	return h.vmsPath() + "/" + vmUUID
}

// CreateVM posts the new VM body. The minimum FusionCompute requires
// for a green provisioning :
//
//   - osOptions  : guest OS hint (the VRM consumes it for paravirt
//                  driver injection ; we pass "linux64" by default)
//   - vmConfig   : cpu / memory / disks subobject
//   - vmFeature  : empty for the minimal case
//   - location   : cluster + datastore + (optional) portgroup
//
// Disks + NICs are NOT attached here — they come via AttachDisk /
// AttachNIC after CreateVM completes.
func (h *dcsHypervisor) CreateVM(ctx context.Context, spec drivers.VMSpec) error {
	if spec.UUID == "" {
		return errors.New("CreateVM: empty uuid")
	}
	body := map[string]any{
		"name":        spec.Name,
		"uuid":        spec.UUID,
		"description": fmt.Sprintf("openweft %s", spec.ProjectUUID),
		"osOptions": map[string]any{
			"osType": "Linux",
			"osVersion": guestOSVersion(spec),
		},
		"vmConfig": map[string]any{
			"cpu": map[string]any{
				"quantity": maxInt(spec.CPUCount, 1),
			},
			"memory": map[string]any{
				"quantityMB": maxInt(spec.MemoryMiB, 512),
			},
			"disks": []any{}, // empty ; disks attach later
			"nics":  []any{},
		},
		"location": map[string]any{
			"cluster":   h.opts.ClusterUUID,
			"datastore": h.opts.DatastoreUUID,
		},
		"autoBoot": false,
	}
	var task vrmTask
	if err := h.client().do(ctx, http.MethodPost, h.vmsPath(), body, &task); err != nil {
		return fmt.Errorf("vrm CreateVM: %w", err)
	}
	if err := h.client().waitTask(ctx, task); err != nil {
		return fmt.Errorf("vrm CreateVM task: %w", err)
	}
	h.log.Info("dcs CreateVM: provisioned", "uuid", spec.UUID, "name", spec.Name)
	return nil
}

// StartVM posts the start action. The VRM returns a task UUID ; we
// wait for the VM to reach "running".
func (h *dcsHypervisor) StartVM(ctx context.Context, vmUUID string) error {
	var task vrmTask
	path := h.vmPath(vmUUID) + "/action/start"
	if err := h.client().do(ctx, http.MethodPost, path, struct{}{}, &task); err != nil {
		return fmt.Errorf("vrm StartVM: %w", err)
	}
	return h.client().waitTask(ctx, task)
}

// StopVM tries a graceful "safe" shutdown ; the caller's ctx
// deadline triggers escalation to "force".
func (h *dcsHypervisor) StopVM(ctx context.Context, vmUUID string) error {
	body := map[string]any{"mode": "safe"}
	var task vrmTask
	path := h.vmPath(vmUUID) + "/action/stop"
	if err := h.client().do(ctx, http.MethodPost, path, body, &task); err != nil {
		// 404 from the VRM means the VM is already gone — idempotent.
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("vrm StopVM: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := h.client().waitTask(waitCtx, task); err != nil {
		// Escalate to force-stop. We don't surface the graceful
		// failure since the operator's intent is "make it stop" ;
		// the force-stop's outcome is what they actually care about.
		h.log.Warn("dcs StopVM: graceful failed, escalating to force", "uuid", vmUUID, "err", err)
		force := map[string]any{"mode": "force"}
		if err := h.client().do(ctx, http.MethodPost, path, force, &task); err != nil {
			return fmt.Errorf("vrm StopVM (force): %w", err)
		}
		return h.client().waitTask(ctx, task)
	}
	return nil
}

// DeleteVM removes the VM AND its attached disks
// (isFormat=true so the VRM reclaims the storage).
// Idempotent : 404 collapses to nil.
func (h *dcsHypervisor) DeleteVM(ctx context.Context, vmUUID string) error {
	var task vrmTask
	path := h.vmPath(vmUUID) + "?isFormat=true"
	if err := h.client().do(ctx, http.MethodDelete, path, nil, &task); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("vrm DeleteVM: %w", err)
	}
	if task.TaskUUID == "" {
		return nil // 204 No Content path — deletion was synchronous
	}
	return h.client().waitTask(ctx, task)
}

// AttachDisk posts the attachvol action with the volume UUID and
// the target bus/sequence.
func (h *dcsHypervisor) AttachDisk(ctx context.Context, vmUUID string, disk drivers.DiskSpec) error {
	body := map[string]any{
		"volUrn": "urn:sites:" + h.opts.SiteUUID + ":volumes:" + disk.VolumeUUID,
		"sequenceNum": diskSequence(disk),
		"isBoot":      disk.Boot,
	}
	var task vrmTask
	path := h.vmPath(vmUUID) + "/action/attachvol"
	if err := h.client().do(ctx, http.MethodPost, path, body, &task); err != nil {
		return fmt.Errorf("vrm AttachDisk: %w", err)
	}
	return h.client().waitTask(ctx, task)
}

// DetachDisk posts detachvol. volumeUUID is the FusionCompute volume
// handle the caller passed in.
func (h *dcsHypervisor) DetachDisk(ctx context.Context, vmUUID, volumeUUID string) error {
	body := map[string]any{
		"volUrn": "urn:sites:" + h.opts.SiteUUID + ":volumes:" + volumeUUID,
	}
	var task vrmTask
	path := h.vmPath(vmUUID) + "/action/detachvol"
	if err := h.client().do(ctx, http.MethodPost, path, body, &task); err != nil {
		return fmt.Errorf("vrm DetachDisk: %w", err)
	}
	return h.client().waitTask(ctx, task)
}

// AttachNIC binds a vNIC to the configured portgroup. NICHandle's
// Device field carries the portgroup UUID.
func (h *dcsHypervisor) AttachNIC(ctx context.Context, vmUUID string, nic drivers.NICHandle) error {
	portgroup := nic.Device
	if portgroup == "" {
		portgroup = h.opts.PortgroupUUID
	}
	body := map[string]any{
		"portGroupUrn": "urn:sites:" + h.opts.SiteUUID + ":dvswitches:portgroups:" + portgroup,
	}
	var task vrmTask
	path := h.vmPath(vmUUID) + "/action/attachnic"
	if err := h.client().do(ctx, http.MethodPost, path, body, &task); err != nil {
		return fmt.Errorf("vrm AttachNIC: %w", err)
	}
	return h.client().waitTask(ctx, task)
}

func (h *dcsHypervisor) DetachNIC(ctx context.Context, vmUUID, nicDevice string) error {
	body := map[string]any{
		"nicUrn": "urn:sites:" + h.opts.SiteUUID + ":nics:" + nicDevice,
	}
	var task vrmTask
	path := h.vmPath(vmUUID) + "/action/detachnic"
	if err := h.client().do(ctx, http.MethodPost, path, body, &task); err != nil {
		return fmt.Errorf("vrm DetachNIC: %w", err)
	}
	return h.client().waitTask(ctx, task)
}

// guestOSVersion derives a FusionCompute-recognised OS version string
// from the weft VMSpec. The VRM uses it to inject paravirt drivers.
// We default to a generic "Other Linux 64-bit" when the spec doesn't
// give us a hint ; FusionCompute lists ~80 specific identifiers (e.g.
// "Ubuntu 22.04 64bit") but the generic fallback boots fine.
func guestOSVersion(spec drivers.VMSpec) string {
	// VMSpec doesn't carry an OS label today ; future weft-proto
	// additions can route the boot.OS field here. For now we always
	// return the safe fallback.
	return "Other Linux 64-bit"
}

// diskSequence maps weft's DiskSpec to a FusionCompute scsi-bus
// sequence number. The boot disk takes slot 0 ; subsequent attaches
// claim 1, 2, ... in order. For the v0.1 path we trust the caller
// to manage uniqueness ; future versions may query the VM to find a
// free slot.
func diskSequence(disk drivers.DiskSpec) int {
	if disk.Boot {
		return 0
	}
	return 1 // FusionCompute auto-bumps duplicates ; the API tolerates the same number on second attach
}

// isNotFound matches the FusionCompute "not found" error response.
// The VRM doesn't use a stable error code — it embeds the message
// in the body's `reason` field — so we string-match on the standard
// pattern.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "status 404") || strings.Contains(s, "not found") || strings.Contains(s, "vmNotFound")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
