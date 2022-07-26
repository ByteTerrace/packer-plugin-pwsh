package main

import (
	"fmt"
	"os"

	"github.com/ByteTerrace/packer-plugin-pwsh/version"
	"github.com/hashicorp/packer-plugin-sdk/plugin"

	pwsh "github.com/ByteTerrace/packer-plugin-pwsh/provisioner"
)

func main() {
	pluginSet := plugin.NewSet()

	pluginSet.RegisterProvisioner(plugin.DEFAULT_NAME, new(pwsh.Provisioner))
	pluginSet.SetVersion(version.PluginVersion)

	error := pluginSet.Run()

	if error != nil {
		fmt.Fprintln(os.Stderr, error.Error())
		os.Exit(1)
	}
}
