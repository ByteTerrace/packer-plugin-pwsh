//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package pwsh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/multistep/commonsteps"
	"github.com/hashicorp/packer-plugin-sdk/retry"
	"github.com/hashicorp/packer-plugin-sdk/shell"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"github.com/hashicorp/packer-plugin-sdk/tmp"
	"github.com/hashicorp/packer-plugin-sdk/uuid"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

const (
	startTimeout = (7 * time.Minute)
)

var psEscape = strings.NewReplacer(
	"$", "`$",
	"\"", "`\"",
	"`", "``",
	"'", "`'",
)

type Config struct {
	shell.Provisioner               `mapstructure:",squash"`
	shell.ProvisionerRemoteSpecific `mapstructure:",squash"`

	ElevatedEnvVarFormat string `mapstructure:"elevated_env_var_format"`
	RemoteEnvVarPath     string `mapstructure:"remote_env_var_path"`

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

	if nil != e {
		return e
	}

	if "" == p.config.ElevatedEnvVarFormat {
		p.config.ElevatedEnvVarFormat = `$env:%s="%s"; `
	}

	if "" == p.config.EnvVarFormat {
		p.config.EnvVarFormat = `$env:%s="%s"; `
	}

	if "" == p.config.ExecuteCommand {
		p.config.ExecuteCommand = fmt.Sprintf(
			`FOR /F \"tokens=* USEBACKQ\" %%F IN (`+"`where pwsh /R \"%%PROGRAMFILES%%\\PowerShell\" ^2^>nul ^|^| where powershell`"+`) DO (\"%%F\" %s);`,
			fmt.Sprintf(`-ExecutionPolicy "Bypass" "%s"`, `. {{.Vars}}; &'{{.Path}}'; exit $LastExitCode }`),
		)
	}

	if (nil != p.config.Inline) && (0 == len(p.config.Inline)) {
		p.config.Inline = nil
	}

	if "" == p.config.RemoteEnvVarPath {
		p.config.RemoteEnvVarPath = fmt.Sprintf(`c:/Windows/Temp/packer-ps-env-vars-%s.ps1`, uuid.TimeOrderedUUID())
	}

	if nil == p.config.Scripts {
		p.config.Scripts = make([]string, 0)
	}

	if nil == p.config.Vars {
		p.config.Vars = make([]string, 0)
	}

	if (nil == p.config.Inline) && (0 == len(p.config.Scripts)) {
		e = packersdk.MultiErrorAppend(e, errors.New("Either a script file or an inline script must be specified."))
	} else if (nil != p.config.Inline) && (0 < len(p.config.Scripts)) {
		e = packersdk.MultiErrorAppend(e, errors.New("Only a script file or an inline script can be specified, not both."))
	}

	if nil != e {
		return e
	}

	return nil
}
func (p *Provisioner) Provision(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, generatedData map[string]interface{}) error {
	p.communicator = comm
	p.generatedData = generatedData

	ui.Say(fmt.Sprintf("%s", p.config.ExecuteCommand))

	return nil
}

func (p *Provisioner) createCommandTextNonPrivileged() (command string, e error) {
	e = p.prepareEnvVars(false)

	if nil != e {
		return "", e
	}

	ctxData := p.generatedData
	ctxData["Path"] = p.config.RemotePath
	ctxData["Vars"] = p.config.RemoteEnvVarPath
	p.config.ctx.Data = ctxData

	command, e = interpolate.Render(p.config.ExecuteCommand, &p.config.ctx)

	if nil != e {
		return "", fmt.Errorf("Error processing command: %s.", e)
	}

	return command, nil
}
func (p *Provisioner) createFlattenedEnvVars(elevated bool) (flattened string) {
	environmentVariables := make(map[string]string)
	flattened = ""

	environmentVariables["PACKER_BUILD_NAME"] = p.config.PackerBuildName
	environmentVariables["PACKER_BUILDER_TYPE"] = p.config.PackerBuilderType

	httpAddress := p.generatedData["PackerHTTPAddr"]
	httpIp := p.generatedData["PackerHTTPIP"]
	httpPort := p.generatedData["PackerHTTPPort"]

	if httpAddress != nil && httpAddress != commonsteps.HttpAddrNotImplemented {
		environmentVariables["PACKER_HTTP_ADDR"] = httpAddress.(string)
	}

	if httpIp != nil && httpIp != commonsteps.HttpIPNotImplemented {
		environmentVariables["PACKER_HTTP_IP"] = httpIp.(string)
	}

	if httpPort != nil && httpPort != commonsteps.HttpPortNotImplemented {
		environmentVariables["PACKER_HTTP_PORT"] = httpPort.(string)
	}

	// interpolate environment variables
	p.config.ctx.Data = p.generatedData

	// split vars into key/value components
	for _, envVar := range p.config.Vars {
		envVar, e := interpolate.Render(envVar, &p.config.ctx)
		if e != nil {
			return
		}
		keyValue := strings.SplitN(envVar, "=", 2)
		// escape chars special to PS in each env var value
		escapedEnvVarValue := psEscape.Replace(keyValue[1])
		if escapedEnvVarValue != keyValue[1] {
			log.Printf("Environment variable %s converted to %s after escaping chars special to PS.", keyValue[1], escapedEnvVarValue)
		}
		environmentVariables[keyValue[0]] = escapedEnvVarValue
	}

	for k, v := range p.config.Env {
		envVarName, e := interpolate.Render(k, &p.config.ctx)

		if e != nil {
			return
		}

		envVarValue, e := interpolate.Render(v, &p.config.ctx)

		if e != nil {
			return
		}

		environmentVariables[envVarName] = psEscape.Replace(envVarValue)
	}

	// create a list of env var keys in sorted order
	var keys []string
	for k := range environmentVariables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	format := p.config.EnvVarFormat

	if elevated {
		format = p.config.ElevatedEnvVarFormat
	}

	// re-assemble vars using OS specific format pattern and flatten
	for _, key := range keys {
		flattened += fmt.Sprintf(format, key, environmentVariables[key])
	}
	return
}
func (p *Provisioner) prepareEnvVars(elevated bool) (e error) {
	e = p.uploadEnvVars(p.createFlattenedEnvVars(elevated))

	if nil != e {
		return e
	}

	return
}
func (p *Provisioner) uploadEnvVars(flattenedEnvVars string) (e error) {
	ctx := context.TODO()
	envVarReader := strings.NewReader(flattenedEnvVars)

	log.Printf("Uploading environment variables to %s.", p.config.RemoteEnvVarPath)

	e = retry.Config{StartTimeout: startTimeout}.Run(ctx, func(context.Context) error {
		if e := p.communicator.Upload(p.config.RemoteEnvVarPath, envVarReader, nil); e != nil {
			return fmt.Errorf("Error uploading script containing environment variables: %s.", e)
		}

		return e
	})

	return
}

func extractScript(p *Provisioner) (string, error) {
	temp, e := tmp.File("pwsh-provisioner")
	if e != nil {
		return "", e
	}
	defer temp.Close()
	writer := bufio.NewWriter(temp)
	for _, command := range p.config.Inline {
		log.Printf("Found command: %s", command)
		if _, e := writer.WriteString(command + "\n"); e != nil {
			return "", fmt.Errorf("Error preparing powershell script: %s.", e)
		}
	}

	if e := writer.Flush(); e != nil {
		return "", fmt.Errorf("Error preparing powershell script: %s.", e)
	}

	return temp.Name(), nil
}
