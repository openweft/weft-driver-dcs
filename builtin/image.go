// image.go — ImageDriver scaffold for FusionCompute. Images live in
// the VRM's "Image Management" service ; the driver pulls from an OCI
// registry, unpacks, and uploads as a FusionCompute template via the
// /service/sites/{site}/templates REST surface.

package builtin

import (
	"context"
	"log/slog"

	drivers "github.com/openweft/weft-drivers"
)

type dcsImage struct {
	opts Options
	log  *slog.Logger
}

func newDCSImage(opts Options) *dcsImage {
	return &dcsImage{opts: opts, log: opts.Logger}
}

func (i *dcsImage) HostInfo(ctx context.Context) (drivers.HostInfo, error) {
	return drivers.HostInfo{
		UUID:     "dcs-" + i.opts.SiteUUID,
		Hostname: i.opts.VRMEndpoint,
	}, nil
}

func (i *dcsImage) Pull(ctx context.Context, ref string) error {
	return drivers.ErrUnsupported
}

func (i *dcsImage) LocalPath(ctx context.Context, ref string) (string, error) {
	return "", drivers.ErrUnsupported
}

func (i *dcsImage) Delete(ctx context.Context, ref string) error {
	return drivers.ErrUnsupported
}

func (i *dcsImage) InCache(ctx context.Context, ref string) (bool, error) {
	return false, drivers.ErrUnsupported
}
