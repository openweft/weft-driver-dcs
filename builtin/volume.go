// volume.go — VolumeDriver implementation for FusionCompute. Volumes
// live as LUN slices on the configured datastore (FC / iSCSI / FCoE
// or IP-SAN backed). Lifecycle goes through the VRM's
// /service/sites/{site}/volumes endpoint ; snapshots / backups route
// through weft-block for cluster-replicated semantics.

package builtin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	drivers "github.com/openweft/weft-drivers"
)

type dcsVolume struct {
	opts Options
	log  *slog.Logger

	mu  sync.Mutex
	vrm *vrmClient
}

func newDCSVolume(opts Options) *dcsVolume {
	return &dcsVolume{opts: opts, log: opts.Logger}
}

func (v *dcsVolume) client() *vrmClient {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vrm == nil {
		v.vrm = newVRMClient(v.opts.VRMEndpoint, v.opts.Username, v.opts.Password, v.opts.Insecure, v.log)
	}
	return v.vrm
}

func (v *dcsVolume) Name() string { return "dcs" }

// Local is false : FusionCompute datastores are cluster-wide through
// the VRM, accessible from any CNA in the site.
func (v *dcsVolume) Local() bool { return false }

func (v *dcsVolume) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{
		UUID:     "dcs-" + v.opts.SiteUUID,
		Hostname: v.opts.VRMEndpoint,
	}, nil
}

func (v *dcsVolume) volumesPath() string {
	return "/service/sites/" + v.opts.SiteUUID + "/volumes"
}

func (v *dcsVolume) volumePath(uuid string) string {
	return v.volumesPath() + "/" + uuid
}

// EnsureVolume POSTs a new volume on the configured datastore. Thin
// provisioning by default ; the operator can flip to thick via a
// future input (FusionCompute supports both "Thin", "Thick", and
// "ThickPrealloc").
func (v *dcsVolume) EnsureVolume(ctx context.Context, spec drivers.VolumeSpec) error {
	body := map[string]any{
		"uuid":              spec.UUID,
		"name":              spec.Name,
		"quantityGB":        spec.SizeGiB,
		"datastoreUrn":      "urn:sites:" + v.opts.SiteUUID + ":datastores:" + v.opts.DatastoreUUID,
		"type":              "normal",
		"isThin":            true,
		"persistentDisk":    true,
	}
	var task vrmTask
	if err := v.client().do(ctx, http.MethodPost, v.volumesPath(), body, &task); err != nil {
		// 409 = already exists. Treat as idempotent success ; the
		// caller might be re-running EnsureVolume after a partial
		// failure.
		if isAlreadyExists(err) {
			v.log.Info("dcs EnsureVolume: already exists", "uuid", spec.UUID)
			return nil
		}
		return fmt.Errorf("vrm EnsureVolume: %w", err)
	}
	if task.TaskUUID != "" {
		if err := v.client().waitTask(ctx, task); err != nil {
			return fmt.Errorf("vrm EnsureVolume task: %w", err)
		}
	}
	v.log.Info("dcs EnsureVolume: provisioned", "uuid", spec.UUID, "size_gib", spec.SizeGiB)
	return nil
}

// DestroyVolume DELETEs the volume. Idempotent : 404 collapses to nil.
func (v *dcsVolume) DestroyVolume(ctx context.Context, volumeUUID string) error {
	var task vrmTask
	if err := v.client().do(ctx, http.MethodDelete, v.volumePath(volumeUUID), nil, &task); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("vrm DestroyVolume: %w", err)
	}
	if task.TaskUUID != "" {
		return v.client().waitTask(ctx, task)
	}
	return nil
}

// AttachVolume returns the VRM-side URN as the BackingPath. The
// HypervisorDriver's AttachDisk consumes this URN when binding to a
// VM, so the round-trip is consistent.
func (v *dcsVolume) AttachVolume(ctx context.Context, volumeUUID, hostUUID string) (drivers.AttachedVolume, error) {
	return drivers.AttachedVolume{
		BackingPath: "urn:sites:" + v.opts.SiteUUID + ":volumes:" + volumeUUID,
	}, nil
}

// DetachVolume is a no-op at the volume layer ; the actual detach
// goes through HypervisorDriver.DetachDisk against the VM that owns
// the binding. FusionCompute doesn't have a "release host" call.
func (v *dcsVolume) DetachVolume(ctx context.Context, volumeUUID, hostUUID string) error {
	return nil
}

// Snapshot / backup operations route through weft-block when the
// operator wants cluster-replicated semantics that survive a DC
// outage. FusionCompute has its own snapshot story (instant snapshots
// via /service/sites/{site}/snapshots) but its retention guarantees
// don't match what weft-block delivers ; we keep this layer stubbed
// to make the routing decision explicit.
func (v *dcsVolume) CreateSnapshot(ctx context.Context, spec drivers.SnapshotSpec) (drivers.Snapshot, error) {
	return drivers.Snapshot{}, drivers.ErrUnsupported
}

func (v *dcsVolume) ListSnapshots(ctx context.Context, volumeUUID string) ([]drivers.Snapshot, error) {
	return nil, drivers.ErrUnsupported
}

func (v *dcsVolume) DeleteSnapshot(ctx context.Context, volumeUUID, snapshotName string) error {
	return drivers.ErrUnsupported
}

func (v *dcsVolume) RevertSnapshot(ctx context.Context, volumeUUID, snapshotName string) error {
	return drivers.ErrUnsupported
}

func (v *dcsVolume) CreateBackup(ctx context.Context, spec drivers.BackupSpec) (drivers.Backup, error) {
	return drivers.Backup{}, drivers.ErrUnsupported
}

func (v *dcsVolume) ListBackups(ctx context.Context, target, volumeUUID string) ([]drivers.Backup, error) {
	return nil, drivers.ErrUnsupported
}

func (v *dcsVolume) DeleteBackup(ctx context.Context, backupURL string) error {
	return drivers.ErrUnsupported
}

func (v *dcsVolume) RestoreBackup(ctx context.Context, backupURL string, spec drivers.VolumeSpec) error {
	return drivers.ErrUnsupported
}

// isAlreadyExists matches the FusionCompute "duplicate UUID" error.
// Like isNotFound, the VRM doesn't expose a stable code ; we
// string-match the body.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "status 409") || strings.Contains(s, "already exists") || strings.Contains(s, "duplicate")
}
