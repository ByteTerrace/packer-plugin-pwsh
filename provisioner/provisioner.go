//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package pwsh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2/hcldec"
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
		p.config.ExecuteCommand = `FOR /F "tokens=* USEBACKQ" %F IN (` + "`where pwsh /R \"%PROGRAMFILES%\\PowerShell\" ^2^>nul ^|^| where powershell`" + `) DO ("%F" -Command "&'{{.Path}}'; exit $LastExitCode;" -ExecutionPolicy "Bypass")`
		//p.config.ExecuteCommand = `powershell -ExecutionPolicy "Bypass" "&'{{.Path}}'; exit $LastExitCode;"`
	}

	if (nil != p.config.Inline) && (0 == len(p.config.Inline)) {
		p.config.Inline = nil
	}

	if "" == p.config.RemoteEnvVarPath {
		p.config.RemoteEnvVarPath = fmt.Sprintf(`c:/Windows/Temp/packer-pwsh-variables-%s.ps1`, uuid.TimeOrderedUUID())
	}

	if "" == p.config.RemotePath {
		p.config.RemotePath = fmt.Sprintf(`c:/Windows/Temp/packer-pwsh-script-%s.ps1`, uuid.TimeOrderedUUID())
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
func (p *Provisioner) Provision(ctx context.Context, ui packersdk.Ui, communicator packersdk.Communicator, generatedData map[string]interface{}) error {
	p.communicator = communicator
	p.generatedData = generatedData

	config := p.config
	inlineScriptFilePath, e := getInlineScriptFilePath(config)
	scripts := make([]string, len(config.Scripts))

	if nil != e {
		return e
	}

	if "" != inlineScriptFilePath {
		defer os.Remove(inlineScriptFilePath)

		scripts = append(scripts, inlineScriptFilePath)
	}

	copy(scripts, config.Scripts)

	contextData := p.generatedData
	contextData["Path"] = config.RemotePath
	contextData["Vars"] = config.RemoteEnvVarPath
	config.ctx.Data = contextData

	command, e := interpolate.Render(config.ExecuteCommand, &config.ctx)

	if nil != e {
		return e
	}

	ui.Say(fmt.Sprintf(`Provisioning with pwsh; command template: %s`, command))

	for _, path := range scripts {
		scriptFileInfo, e := os.Stat(path)

		if nil != e {
			return fmt.Errorf("Error stating PowerShell script: %s.", e)
		}

		ui.Say(fmt.Sprintf(`Provisioning with pwsh; script path: %s`, path))

		if os.IsPathSeparator(config.RemotePath[len(config.RemotePath)-1]) {
			config.RemotePath += filepath.Base(scriptFileInfo.Name())
		}

		scriptFileHandle, e := os.Open(path)

		if nil != e {
			return fmt.Errorf("Error opening PowerShell script: %s.", e)
		}

		defer scriptFileHandle.Close()

		e = retry.Config{
			StartTimeout: (23 * time.Minute),
			Tries:        3,
		}.Run(
			ctx,
			getUploadAndExecuteScriptFunc(
				command,
				communicator,
				config,
				scriptFileHandle,
				&scriptFileInfo,
				ui,
			),
		)

		if e != nil {
			return e
		}

		scriptFileHandle.Close()
	}

	return nil
}

func getInlineScriptFilePath(config Config) (string, error) {
	const preparationErrorTemplate = "Error preparing PowerShell script: %s."

	if (nil == config.Inline) || (0 == len(config.Inline)) {
		return "", nil
	}

	scriptFileHandle, e := tmp.File("pwsh-provisioner")

	if nil != e {
		return "", e
	}

	defer scriptFileHandle.Close()

	writer := bufio.NewWriter(scriptFileHandle)

	for _, command := range config.Inline {
		if _, e := writer.WriteString(command + "\n"); nil != e {
			return "", fmt.Errorf(preparationErrorTemplate, e)
		}
	}

	if e := writer.Flush(); nil != e {
		return "", fmt.Errorf(preparationErrorTemplate, e)
	}

	return scriptFileHandle.Name(), nil
}
func getUploadAndExecuteScriptFunc(command string, communicator packersdk.Communicator, config Config, scriptFileHandle *os.File, scriptFileInfo *os.FileInfo, ui packersdk.Ui) (fn func(context.Context) error) {
	return func(context context.Context) error {
		if _, e := scriptFileHandle.Seek(0, 0); nil != e {
			return e
		}

		if e := communicator.Upload(config.RemotePath, scriptFileHandle, scriptFileInfo); nil != e {
			return fmt.Errorf("Error uploading script: %s.", e)
		}

		remoteCmd := &packersdk.RemoteCmd{Command: command}

		if e := remoteCmd.RunWithUi(context, communicator, ui); nil != e {
			return e
		}

		ui.Say(fmt.Sprintf("Provisioning with pwsh; exit code: %d.", remoteCmd.ExitStatus()))

		if e := config.ValidExitCode(remoteCmd.ExitStatus()); nil != e {
			return e
		}

		return nil
	}
}
