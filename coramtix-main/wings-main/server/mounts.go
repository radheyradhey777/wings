package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/apex/log"

	"github.com/coramtix-dev/wings/config"
	"github.com/coramtix-dev/wings/environment"
)

// To avoid confusion when working with mounts, assume that a server.Mount has not been properly
// cleaned up and had the paths set. An environment.Mount should only be returned with valid paths
// that have been checked.
type Mount environment.Mount

// Returns the default container mounts for the server instance. This includes the data directory
// for the server. Previously this would also mount in host timezone files, however we've moved from
// that approach to just setting `TZ=Timezone` environment values in containers which should work
// in most scenarios.
func (s *Server) Mounts() []environment.Mount {
	m := []environment.Mount{
		{
			Default:  true,
			Target:   "/home/container",
			Source:   s.Filesystem().Path(),
			ReadOnly: false,
		},
	}

	if config.Get().System.User.Passwd {
		passwdMount := environment.Mount{
			Default:  true,
			Target:   "/etc/passwd",
			Source:   config.Get().System.User.PasswdFile,
			ReadOnly: true,
		}

		m = append(m, passwdMount)
	}
	// Also include any of this server's custom mounts when returning them.
	return append(m, s.customMounts()...)
}

// Returns the custom mounts for a given server after verifying that they are within a list of
// allowed mount points for the node.
func (s *Server) customMounts() []environment.Mount {
	var mounts []environment.Mount

	for _, m := range s.Config().Mounts {
		source := filepath.Clean(m.Source)
		target := filepath.Clean(m.Target)

		logger := s.Log().WithFields(log.Fields{
			"source_path": source,
			"target_path": target,
			"read_only":   m.ReadOnly,
		})

		// Check if the source path exists
		if _, err := os.Stat(source); os.IsNotExist(err) {
			logger.WithField("missing_source_path", source).Warn("skipping custom server mount, source path does not exist")
			continue
		}

		// Check if the target path includes /home/container
		if strings.HasPrefix(target, "/home/container") && config.Get().BlockBaseDirMount {
			logger.WithField("invalid_target_path", target).Warn("Skipping custom server mount; target path includes /home/container")
			continue
		}

		mounted := false
		for _, allowed := range config.Get().AllowedMounts {
			// Check if the source path is included in the allowed mounts list.
			// filepath.Clean will strip all trailing slashes (unless the path is a root directory).
			if !strings.HasPrefix(source, filepath.Clean(allowed)) {
				continue
			}

			mounted = true
			mounts = append(mounts, environment.Mount{
				Source:   source,
				Target:   target,
				ReadOnly: m.ReadOnly,
			})

			break
		}

		if !mounted {
			logger.Warn("skipping custom server mount, not in list of allowed mount points")
		}
	}

	return mounts
}
