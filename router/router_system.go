package router

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/apex/log"
	"github.com/gin-gonic/gin"

	"github.com/coramtix-dev/wings/config"
	"github.com/coramtix-dev/wings/router/middleware"
	"github.com/coramtix-dev/wings/server"
	"github.com/coramtix-dev/wings/server/installer"
	"github.com/coramtix-dev/wings/system"
)

// Returns information about the system that wings is running on.
func getSystemInformation(c *gin.Context) {
	i, err := system.GetSystemInformation()
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}

	if c.Query("v") == "2" {
		c.JSON(http.StatusOK, i)
		return
	}

	c.JSON(http.StatusOK, struct {
		Architecture  string `json:"architecture"`
		CPUCount      int    `json:"cpu_count"`
		KernelVersion string `json:"kernel_version"`
		OS            string `json:"os"`
		Version       string `json:"version"`
	}{
		Architecture:  i.System.Architecture,
		CPUCount:      i.System.CPUThreads,
		KernelVersion: i.System.KernelVersion,
		OS:            i.System.OSType,
		Version:       i.Version,
	})
}

// Returns list of host machine IP addresses
func getSystemIps(c *gin.Context) {
	interfaces, err := system.GetSystemIps()
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}

	// Append config defined ips as well
	for i := range config.Get().Docker.SystemIps {
		targetIp := config.Get().Docker.SystemIps[i]
		if slices.Contains(interfaces, targetIp) {
			continue
		}

		interfaces = append(interfaces, targetIp)
	}

	c.JSON(http.StatusOK, &system.IpAddresses{IpAddresses: interfaces})
}

// Returns resource utilization info for the system wings is running on.
func getSystemUtilization(c *gin.Context) {
	cfg := config.Get()
	u, err := system.GetSystemUtilization(
		cfg.System.RootDirectory,
		cfg.System.LogDirectory,
		cfg.System.Data,
		cfg.System.ArchiveDirectory,
		cfg.System.BackupDirectory,
		cfg.System.TmpDirectory,
	)
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

// Returns docker disk utilization
func getDockerDiskUsage(c *gin.Context) {
	d, err := system.GetDockerDiskUsage(c)
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

// Prunes the docker image cache
func pruneDockerImages(c *gin.Context) {
	p, err := system.PruneDockerImages(c)
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// Returns all the servers that are registered and configured correctly on
// this wings instance.
func getAllServers(c *gin.Context) {
	servers := middleware.ExtractManager(c).All()
	out := make([]server.APIResponse, len(servers), len(servers))
	for i, v := range servers {
		out[i] = v.ToAPIResponse()
	}
	c.JSON(http.StatusOK, out)
}

// Creates a new server on the wings daemon and begins the installation process
// for it.
func postCreateServer(c *gin.Context) {
	manager := middleware.ExtractManager(c)

	details := installer.ServerDetails{}
	if err := c.BindJSON(&details); err != nil {
		return
	}

	install, err := installer.New(c.Request.Context(), manager, details)
	if err != nil {
		if installer.IsValidationError(err) {
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error": "The data provided in the request could not be validated.",
			})
			return
		}

		middleware.CaptureAndAbort(c, err)
		return
	}

	// Plop that server instance onto the request so that it can be referenced in
	// requests from here-on out.
	manager.Add(install.Server())

	// Begin the installation process in the background to not block the request
	// cycle. If there are any errors they will be logged and communicated back
	// to the Panel where a reinstall may take place.
	go func(i *installer.Installer) {
		if err := i.Server().CreateEnvironment(); err != nil {
			i.Server().Log().WithField("error", err).Error("failed to create server environment during install process")
			return
		}

		if err := i.Server().Install(); err != nil {
			log.WithFields(log.Fields{"server": i.Server().ID(), "error": err}).Error("failed to run install process for server")
			return
		}

		if i.StartOnCompletion {
			log.WithField("server_id", i.Server().ID()).Debug("starting server after successful installation")
			if err := i.Server().HandlePowerAction(server.PowerActionStart, 30); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					log.WithFields(log.Fields{"server_id": i.Server().ID(), "action": "start"}).Warn("could not acquire a lock while attempting to perform a power action")
				} else {
					log.WithFields(log.Fields{"server_id": i.Server().ID(), "action": "start", "error": err}).Error("encountered error processing a server power action in the background")
				}
			}
		} else {
			log.WithField("server_id", i.Server().ID()).Debug("skipping automatic start after successful server installation")
		}
	}(install)

	c.Status(http.StatusAccepted)
}

type postUpdateConfigurationResponse struct {
	Applied bool `json:"applied"`
}

// Updates the running configuration for this Wings instance.
func postUpdateConfiguration(c *gin.Context) {
	cfg := config.Get()

	if cfg.IgnorePanelConfigUpdates {
		c.JSON(http.StatusOK, postUpdateConfigurationResponse{
			Applied: false,
		})
		return
	}

	if err := c.BindJSON(&cfg); err != nil {
		return
	}

	// Keep the SSL certificates the same since the Panel will send through Lets Encrypt
	// default locations. However, if we picked a different location manually we don't
	// want to override that.
	//
	// If you pass through manual locations in the API call this logic will be skipped.
	if strings.HasPrefix(cfg.Api.Ssl.KeyFile, "/etc/letsencrypt/live/") {
		cfg.Api.Ssl.KeyFile = config.Get().Api.Ssl.KeyFile
		cfg.Api.Ssl.CertificateFile = config.Get().Api.Ssl.CertificateFile
	}

	// Try to write this new configuration to the disk before updating our global
	// state with it.
	if err := config.WriteToDisk(cfg); err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	// Since we wrote it to the disk successfully now update the global configuration
	// state to use this new configuration struct.
	config.Set(cfg)
	c.JSON(http.StatusOK, postUpdateConfigurationResponse{
		Applied: true,
	})
}
