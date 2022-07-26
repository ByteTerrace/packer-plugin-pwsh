//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package pwsh

import (
	"context"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/shell"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

type Config struct {
	shell.Provisioner               `mapstructure:",squash"`
	shell.ProvisionerRemoteSpecific `mapstructure:",squash"`

	ctx interpolate.Context
}
type Provisioner struct {
	config        Config
	communicator  packersdk.Communicator
	generatedData map[string]interface{}
}

func (p *Provisioner) ConfigSpec() hcldec.ObjectSpec { return p.config.FlatMapstructure().HCL2Spec() }
func (p *Provisioner) Prepare(raws ...interface{}) error {
	e := config.Decode(
		&p.config,
		&config.DecodeOpts{
			DecodeHooks:        append(config.DefaultDecodeHookFuncs),
			Interpolate:        true,
			InterpolateContext: &p.config.ctx,
			InterpolateFilter: &interpolate.RenderFilter{
				Exclude: []string{
					"execute_command",
				},
			},
			PluginType: "pwsh",
		},
		raws...,
	)

	if e != nil {
		return e
	}

	if p.config.ExecuteCommand == "" {
		p.config.ExecuteCommand = "pwsh -Command \"Write-Host 'Hello packer!;'\" -ExecutionPolicy \"Bypass\""
	}

	return nil
}
func (p *Provisioner) Provision(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, generatedData map[string]interface{}) error {
	p.communicator = comm
	p.generatedData = generatedData

	return nil
}
