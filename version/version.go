package version

import "github.com/hashicorp/packer-plugin-sdk/version"

var (
	PluginVersion     = version.InitializePluginVersion(Version, VersionPrerelease)
	Version           = "0.0.0"
	VersionPrerelease = "preview"
)
