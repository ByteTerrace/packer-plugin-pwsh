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

	e := pluginSet.Run()

	if e != nil {
		fmt.Fprintln(os.Stderr, e.Error())
		os.Exit(1)
	}
}
