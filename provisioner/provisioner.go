//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package pwsh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	defaultStartTimeout = (7 * time.Minute)
	defaultTries        = 1
)

type Config struct {
	shell.Provisioner               `mapstructure:",squash"`
	shell.ProvisionerRemoteSpecific `mapstructure:",squash"`

	ElevatedEnvVarFormat string `mapstructure:"elevated_env_var_format"`
	PwshMsiUri           string `mapstructure:"pwsh_msi_uri"`
	PwshUpdateCommand    string `mapstructure:"pwsh_update_command"`
	PwshUpdateScript     string `mapstructure:"pwsh_update_script"`
	RemoteEnvVarPath     string `mapstructure:"remote_env_var_path"`
	RemotePwshUpdatePath string `mapstructure:"remote_pwsh_update_path"`

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

	var defaultElevatedEnvVarFormat string
	var defaultEnvVarFormat string
	var defaultExecuteCommand string
	var defaultPwshMsiUri string
	var defaultPwshUpdateCommand string
	var defaultPwshUpdateScript string
	var defaultRemoteEnvVarPathFormat string
	var defaultRemotePathFormat string
	var defaultRemotePwshUpdatePath string

	switch runtime.GOOS {
	case "linux":
		defaultElevatedEnvVarFormat = "%s='%s'"
		defaultEnvVarFormat = "%s='%s'"
		defaultExecuteCommand = `pwsh -Command "&'{{.Path}}'; exit $LastExitCode;" -ExecutionPolicy "Bypass"`
		defaultPwshMsiUri = ""
		defaultPwshUpdateCommand = "chmod +x {{.Path}}; {{.Path}}"
		defaultPwshUpdateScript = ""
		defaultRemoteEnvVarPathFormat = `/tmp/packer-pwsh-variables-%s.ps1`
		defaultRemotePathFormat = `/tmp/packer-pwsh-script-%s.ps1`
		defaultRemotePwshUpdatePath = `/tmp/packer-pwsh-installer-%s.sh`
	case "windows":
		defaultElevatedEnvVarFormat = `${Env:%s}="%s"`
		defaultEnvVarFormat = `{$Env:%s}="%s"`
		defaultExecuteCommand = `FOR /F "tokens=* USEBACKQ" %F IN (` + "`where pwsh /R \"%PROGRAMFILES%\\PowerShell\" ^2^>nul ^|^| where powershell`" + `) DO ("%F" -Command "&'{{.Path}}'; exit $LastExitCode;" -ExecutionPolicy "Bypass")`
		defaultPwshMsiUri = "https://github.com/PowerShell/PowerShell/releases/download/v7.2.5/PowerShell-7.2.5-win-x64.msi"
		defaultPwshUpdateCommand = defaultExecuteCommand
		defaultPwshUpdateScript = "$ErrorActionPreference = [Management.Automation.ActionPreference]::Stop;\n"
		defaultPwshUpdateScript += "$exitCode = -1;\n"
		defaultPwshUpdateScript += "try {\n"
		defaultPwshUpdateScript += "    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12;\n"
		defaultPwshUpdateScript += "    $tempFilePath = ('{0}packer-pwsh-installer.msi' -f [IO.Path]::GetTempPath());\n"
		defaultPwshUpdateScript += fmt.Sprintf("    Invoke-WebRequest -OutFile $tempFilePath -Uri '%s';\n", p.config.PwshMsiUri)
		defaultPwshUpdateScript += "    $exitCode = (Start-Process -ArgumentList @('/i', $tempFilePath, '/norestart', '/qn') -FilePath 'msiexec.exe' -PassThru -Wait).ExitCode;\n"
		defaultPwshUpdateScript += "}\n"
		defaultPwshUpdateScript += "finally {\n"
		defaultPwshUpdateScript += "    exit $exitCode;\n"
		defaultPwshUpdateScript += "}\n"
		defaultRemoteEnvVarPathFormat = `C:/Windows/Temp/packer-pwsh-variables-%s.ps1`
		defaultRemotePathFormat = `C:/Windows/Temp/packer-pwsh-script-%s.ps1`
		defaultRemotePwshUpdatePath = `C:/Windows/Temp/packer-pwsh-installer-%s.ps1`
	default:
		packersdk.MultiErrorAppend(e, fmt.Errorf("Unsupported operating system detected: %s.", runtime.GOOS))
	}

	if "" == p.config.ElevatedEnvVarFormat {
		p.config.ElevatedEnvVarFormat = defaultElevatedEnvVarFormat
	}

	if "" == p.config.EnvVarFormat {
		p.config.EnvVarFormat = defaultEnvVarFormat
	}

	if "" == p.config.ExecuteCommand {
		p.config.ExecuteCommand = defaultExecuteCommand
	}

	if (nil != p.config.Inline) && (0 == len(p.config.Inline)) {
		p.config.Inline = nil
	}

	if ("" == p.config.PwshMsiUri) && ("" != defaultPwshMsiUri) {
		p.config.PwshMsiUri = defaultPwshMsiUri
	}

	if "" == p.config.PwshUpdateCommand {
		p.config.PwshUpdateCommand = defaultPwshUpdateCommand
	}

	if "" == p.config.PwshUpdateScript {
		p.config.PwshUpdateCommand = defaultPwshUpdateScript
	}

	if "" == p.config.RemoteEnvVarPath {
		p.config.RemoteEnvVarPath = fmt.Sprintf(defaultRemoteEnvVarPathFormat, uuid.TimeOrderedUUID())
	}

	if "" == p.config.RemotePath {
		p.config.RemotePath = fmt.Sprintf(defaultRemotePathFormat, uuid.TimeOrderedUUID())
	}

	if "" == p.config.RemotePwshUpdatePath {
		p.config.PwshUpdateCommand = defaultRemotePwshUpdatePath
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

	contextData := p.generatedData
	inlineScriptFilePath, e := p.getInlineScriptFilePath()
	scripts := make([]string, len(p.config.Scripts))

	if nil != e {
		return e
	}

	if "" != inlineScriptFilePath {
		defer os.Remove(inlineScriptFilePath)

		scripts = append(scripts, inlineScriptFilePath)
	}

	copy(scripts, p.config.Scripts)

	p.config.ctx.Data = contextData

	contextData["Path"] = p.config.RemotePath
	contextData["Vars"] = p.config.RemoteEnvVarPath

	if "" != p.config.PwshMsiUri {
		if e = p.updatePowerShellInstallation(ctx, ui); nil != e {
			return e
		}
	}

	contextData["Path"] = p.config.RemotePwshUpdatePath

	if command, e := interpolate.Render(p.config.ExecuteCommand, &p.config.ctx); nil != e {
		return e
	} else {
		ui.Say(fmt.Sprintf(`Provisioning with pwsh; command template: %s`, command))

		if e = p.uploadAndExecuteScripts(
			command,
			ctx,
			scripts,
			ui,
		); nil != e {
			return e
		}
	}

	return nil
}

func (p *Provisioner) getInlineScriptFilePath() (string, error) {
	const preparationErrorTemplate = "Error preparing PowerShell script: %s."

	if (nil == p.config.Inline) || (0 == len(p.config.Inline)) {
		return "", nil
	}

	scriptFileHandle, e := tmp.File("pwsh-provisioner")

	if nil != e {
		return "", e
	}

	defer scriptFileHandle.Close()

	writer := bufio.NewWriter(scriptFileHandle)

	for _, command := range p.config.Inline {
		if _, e := writer.WriteString(command + "\n"); nil != e {
			return "", fmt.Errorf(preparationErrorTemplate, e)
		}
	}

	if e := writer.Flush(); nil != e {
		return "", fmt.Errorf(preparationErrorTemplate, e)
	}

	scriptFileHandle.Close()

	return scriptFileHandle.Name(), nil
}
func (p *Provisioner) getUploadAndExecuteScriptFunc(command string, scriptFileHandle *os.File, scriptFileInfo *os.FileInfo, ui packersdk.Ui) (fn func(context.Context) error) {
	return func(context context.Context) error {
		if _, e := scriptFileHandle.Seek(0, 0); nil != e {
			return e
		}

		if e := p.communicator.Upload(p.config.RemotePath, scriptFileHandle, scriptFileInfo); nil != e {
			return fmt.Errorf("Error uploading script: %s.", e)
		}

		remoteCmd := &packersdk.RemoteCmd{Command: command}

		if e := remoteCmd.RunWithUi(context, p.communicator, ui); nil != e {
			return e
		}

		ui.Say(fmt.Sprintf("Provisioning with pwsh; exit code: %d", remoteCmd.ExitStatus()))

		if e := p.config.ValidExitCode(remoteCmd.ExitStatus()); nil != e {
			return e
		}

		return nil
	}
}
func (p *Provisioner) updatePowerShellInstallation(context context.Context, ui packersdk.Ui) error {
	const preparationErrorTemplate = "Error preparing PowerShell script: %s."

	if (nil == p.config.Inline) || (0 == len(p.config.Inline)) {
		return nil
	}

	scriptFileHandle, e := tmp.File("pwsh-provisioner")

	if nil != e {
		return e
	}

	defer scriptFileHandle.Close()

	writer := bufio.NewWriter(scriptFileHandle)

	command, e := interpolate.Render(p.config.PwshUpdateCommand, &p.config.ctx)

	if nil != e {
		return e
	}

	if _, e = writer.WriteString(p.config.PwshUpdateScript); nil != e {
		return fmt.Errorf(preparationErrorTemplate, e)
	}

	if e = writer.Flush(); nil != e {
		return fmt.Errorf(preparationErrorTemplate, e)
	}

	scriptFileHandle.Close()

	if e = p.uploadAndExecuteScripts(
		command,
		context,
		([]string{scriptFileHandle.Name()}),
		ui,
	); nil != e {
		return e
	}

	return nil
}
func (p *Provisioner) uploadAndExecuteScripts(command string, context context.Context, scripts []string, ui packersdk.Ui) error {
	for _, path := range scripts {
		scriptFileInfo, e := os.Stat(path)

		if nil != e {
			return fmt.Errorf("Error stating PowerShell script: %s.", e)
		}

		ui.Say(fmt.Sprintf("Provisioning with pwsh; script path: %s", path))

		if os.IsPathSeparator(p.config.RemotePath[len(p.config.RemotePath)-1]) {
			p.config.RemotePath += filepath.Base(scriptFileInfo.Name())
		}

		scriptFileHandle, e := os.Open(path)

		if nil != e {
			return fmt.Errorf("Error opening PowerShell script: %s.", e)
		}

		defer scriptFileHandle.Close()

		e = retry.Config{
			StartTimeout: defaultStartTimeout,
			Tries:        defaultTries,
		}.Run(
			context,
			p.getUploadAndExecuteScriptFunc(
				command,
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
