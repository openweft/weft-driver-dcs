// Command weft-driver-dcs is the Huawei FusionCompute / UVP hypervisor
// driver as an external weft go-plugin. The plugin handshake is in
// place ; the FusionCompute REST integration is a follow-on (every
// driver method returns drivers.ErrUnsupported until that ships — see
// the README).
//
// Launched by weft with no arguments, it serves the four driver services
// over go-plugin gRPC. Launch-time config arrives via env, set by the
// launching weft-agent when the operator points `drivers { dcs = ... }`
// at this binary in cluster.hcl.
package main

import (
	"log/slog"
	"os"

	weftplugin "github.com/openweft/weft-driver-plugin"
	dcsdriver "github.com/openweft/weft-driver-dcs/builtin"
	weftslognats "github.com/openweft/weft-slognats"
)

// Env vars the host passes through. VRM endpoint + credentials are
// required ; site / cluster / datastore / portgroup defaults can be
// pinned per-driver or left empty (the spec overrides them per VM).
const (
	envVRMEndpoint   = "WEFT_DCS_VRM_ENDPOINT"
	envUsername      = "WEFT_DCS_USERNAME"
	envPassword      = "WEFT_DCS_PASSWORD"
	envSiteUUID      = "WEFT_DCS_SITE_UUID"
	envClusterUUID   = "WEFT_DCS_CLUSTER_UUID"
	envDatastoreUUID = "WEFT_DCS_DATASTORE_UUID"
	envPortgroupUUID = "WEFT_DCS_PORTGROUP_UUID"
	envInsecure      = "WEFT_DCS_INSECURE_TLS"
)

func main() {
	hostUUID := os.Getenv(weftplugin.EnvHostUUID)
	logger, logCloser := weftslognats.SetupFromEnv("weft.driver.dcs." + hostUUID + ".log")
	defer logCloser.Close()
	slog.SetDefault(logger)

	b, err := dcsdriver.NewBundle(dcsdriver.Options{
		VRMEndpoint:   os.Getenv(envVRMEndpoint),
		Username:      os.Getenv(envUsername),
		Password:      os.Getenv(envPassword),
		SiteUUID:      os.Getenv(envSiteUUID),
		ClusterUUID:   os.Getenv(envClusterUUID),
		DatastoreUUID: os.Getenv(envDatastoreUUID),
		PortgroupUUID: os.Getenv(envPortgroupUUID),
		Insecure:      os.Getenv(envInsecure) == "1",
		Logger:        logger,
	})
	if err != nil {
		logger.Error("weft-driver-dcs : bundle init failed", "err", err)
		os.Exit(1)
	}
	weftplugin.Serve(weftplugin.DriverSet{
		Hypervisor: b.Hypervisor,
		Network:    b.Network,
		Volume:     b.Volume,
		Image:      b.Image,
	})
}
