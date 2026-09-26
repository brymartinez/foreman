package main

import "runtime/debug"

const developmentVersion = "0.1.0-dev"

func buildVersion(version string, info *debug.BuildInfo) string {
	if version != developmentVersion {
		return version
	}
	revision, dirty := "unknown", false
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					revision = setting.Value
					if len(revision) > 12 {
						revision = revision[:12]
					}
				}
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
	}
	if dirty {
		revision += "-dirty"
	}
	return version + " (" + revision + ")"
}
