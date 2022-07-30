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
	"github.com/hashicorp/packer-plugin-sdk/guestexec"
	"github.com/hashicorp/packer-plugin-sdk/retry"
	"github.com/hashicorp/packer-plugin-sdk/shell"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"github.com/hashicorp/packer-plugin-sdk/tmp"
	"github.com/hashicorp/packer-plugin-sdk/uuid"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

const (
	defaultStartTimeout            = (7 * time.Minute)
	defaultTries                   = 1
	pwshScriptClosingErrorFormat   = "Error closing PowerShell script: %s."
	pwshScriptOpeningErrorFormat   = "Error opening PowerShell script: %s."
	pwshScriptPreparingErrorFormat = "Error preparing PowerShell script: %s."
	pwshScriptRemovingErrorFormat  = "Error removing PowerShell script: %s."
	pwshScriptStatingErrorFormat   = "Error stating PowerShell script: %s."
	pwshScriptUploadingErrorFormat = "Error uploading PowerShell script: %s."
)

type Config struct {
	shell.Provisioner               `mapstructure:",squash"`
	shell.ProvisionerRemoteSpecific `mapstructure:",squash"`

	ElevatedEnvVarFormat               string `mapstructure:"elevated_env_var_format"`
	ElevatedExecuteCommand             string `mapstructure:"elevated_execute_command"`
	ElevatedUser                       string `mapstructure:"elevated_user"`
	ElevatedPassword                   string `mapstructure:"elevated_password"`
	PostScriptExecutionRebootIsEnabled bool   `mapstructure:"post_script_execution_reboot_is_enabled"`
	PostProvisionRebootIsEnabled       bool   `mapstructure:"post_provision_reboot_is_enabled"`
	PwshAutoUpdateCommand              string `mapstructure:"pwsh_autoupdate_command"`
	PwshAutoUpdateIsEnabled            bool   `mapstructure:"pwsh_autoupdate_is_enabled"`
	PwshAutoUpdateScript               string `mapstructure:"pwsh_autoupdate_script"`
	PwshInstallerUri                   string `mapstructure:"pwsh_installer_uri"`
	RebootCompleteCommand              string `mapstructure:"reboot_complete_command"`
	RebootInitiateCommand              string `mapstructure:"reboot_initiate_command"`
	RebootPendingCommand               string `mapstructure:"reboot_pending_command"`
	RebootValidateCommand              string `mapstructure:"reboot_validate_command"`
	RemoteEnvVarPath                   string `mapstructure:"remote_env_var_path"`
	RemotePwshAutoUpdatePath           string `mapstructure:"remote_pwsh_autoupdate_path"`

	ctx interpolate.Context
}
type Provisioner struct {
	config        Config
	communicator  packersdk.Communicator
	generatedData map[string]interface{}
}

func (p *Provisioner) Communicator() packersdk.Communicator {
	return p.communicator
}
func (p *Provisioner) ConfigSpec() hcldec.ObjectSpec { return p.config.FlatMapstructure().HCL2Spec() }
func (p *Provisioner) ElevatedUser() string {
	return p.config.ElevatedUser
}
func (p *Provisioner) ElevatedPassword() string {
	elevatedPassword, _ := interpolate.Render(p.config.ElevatedPassword, &p.config.ctx)

	return elevatedPassword
}
func (p *Provisioner) Prepare(raws ...interface{}) error {
	e := config.Decode(
		&p.config,
		&config.DecodeOpts{
			DecodeHooks:        append(config.DefaultDecodeHookFuncs),
			Interpolate:        true,
			InterpolateContext: &p.config.ctx,
			InterpolateFilter: &interpolate.RenderFilter{
				Exclude: []string{
					"elevated_execute_command",
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
	var defaultPwshAutoUpdateCommand string
	var defaultPwshAutoUpdateScriptFormat string
	var defaultPwshInstallerUri string
	var defaultRebootCompleteCommand string
	var defaultRebootInitiateCommand string
	var defaultRebootPendingCommand string
	var defaultRebootValidateCommand string
	var defaultRemoteEnvVarPathFormat string
	var defaultRemotePwshAutoUpdateScriptPathFormat string
	var defaultRemoteScriptPathFormat string

	switch runtime.GOOS {
	//case "linux":
	//	defaultElevatedEnvVarFormat = "%s='%s'"
	//	defaultEnvVarFormat = "%s='%s'"
	//	defaultExecuteCommand = `pwsh -Command "&'{{.Path}}'; exit $LastExitCode;" -ExecutionPolicy "Bypass"`
	//	defaultPwshInstallerUri = ""
	//	defaultPwshUpdateCommand = "chmod +x {{.Path}}; {{.Path}}"
	//	defaultPwshUpdateScriptFormat = ""
	//	defaultRemoteEnvVarPathFormat = `/tmp/packer-pwsh-variables-%s.ps1`
	//	defaultRemotePathFormat = `/tmp/packer-pwsh-script-%s.ps1`
	//	defaultRemotePwshUpdatePathFormat = `/tmp/packer-pwsh-installer-%s.sh`
	case "windows":
		defaultElevatedEnvVarFormat = `${Env:%s}="%s"`
		defaultEnvVarFormat = `{$Env:%s}="%s"`
		defaultExecuteCommand = `FOR /F "tokens=* USEBACKQ" %F IN (` + "`where pwsh /R \"%PROGRAMFILES%\\PowerShell\" ^2^>nul ^|^| where powershell`" + `) DO ("%F" -Command "&'{{.Path}}'; exit $LastExitCode;" -ExecutionPolicy "Bypass")`
		defaultPwshAutoUpdateCommand = defaultExecuteCommand
		defaultPwshAutoUpdateScriptFormat = "$ErrorActionPreference = [Management.Automation.ActionPreference]::Stop;\n"
		defaultPwshAutoUpdateScriptFormat += "$exitCode = -1;\n"
		defaultPwshAutoUpdateScriptFormat += "try {\n"
		defaultPwshAutoUpdateScriptFormat += "    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12;\n"
		defaultPwshAutoUpdateScriptFormat += "    $tempFilePath = ('{0}packer-pwsh-installer.msi' -f [IO.Path]::GetTempPath());\n"
		defaultPwshAutoUpdateScriptFormat += "    Invoke-WebRequest -OutFile $tempFilePath -Uri '%s';\n"
		defaultPwshAutoUpdateScriptFormat += "    $exitCode = (Start-Process -ArgumentList @('/i', $tempFilePath, '/norestart', '/qn') -FilePath 'msiexec.exe' -PassThru -Wait).ExitCode;\n"
		defaultPwshAutoUpdateScriptFormat += "}\n"
		defaultPwshAutoUpdateScriptFormat += "finally {\n"
		defaultPwshAutoUpdateScriptFormat += "    exit $exitCode;\n"
		defaultPwshAutoUpdateScriptFormat += "}\n"
		defaultPwshInstallerUri = "https://github.com/PowerShell/PowerShell/releases/download/v7.2.5/PowerShell-7.2.5-win-x64.msi"
		defaultRebootCompleteCommand = `shutdown /r /f /t 60 /c "packer reboot test"`
		defaultRebootInitiateCommand = `shutdown /r /f /t 0 /c "packer reboot"`
		defaultRebootPendingCommand = "$activeComputerName = (Get-ItemProperty -Name 'ComputerName' -Path 'HKLM:/SYSTEM/CurrentControlSet/Control/ComputerName/ActiveComputerName').ComputerName;"
		defaultRebootPendingCommand += "$componentBasedServicingProperties = (Get-Item -Path 'HKLM:/SOFTWARE/Microsoft/Windows/CurrentVersion/Component Based Servicing').Property;"
		defaultRebootPendingCommand += "$pendingComputerName = (Get-ItemProperty -Name 'ComputerName' -Path 'HKLM:/SYSTEM/CurrentControlSet/Control/ComputerName/ComputerName').ComputerName;"
		defaultRebootPendingCommand += "$sessionManagerProperties = (Get-Item -Path 'HKLM:/System/CurrentControlSet/Control/Session Manager').Property;"
		defaultRebootPendingCommand += "$systemNetLogonProperties = (Get-Item -Path 'HKLM:/SYSTEM/CurrentControlSet/Services/Netlogon').Property;"
		defaultRebootPendingCommand += "$windowsUpdateProperties = (Get-Item -Path 'HKLM:/SOFTWARE/Microsoft/Windows/CurrentVersion/WindowsUpdate/Auto Update').Property;"
		defaultRebootPendingCommand += "$isRebootPending = (($componentBasedServicingProperties -contains 'PackagesPending') -or ($componentBasedServicingProperties -contains 'RebootInProgress') -or ($componentBasedServicingProperties -contains 'RebootPending'));"
		defaultRebootPendingCommand += "$isRebootPending = (($windowsUpdateProperties -contains 'PostRebootReporting') -or ($windowsUpdateProperties -contains 'RebootRequired') -or $isRebootPending);"
		defaultRebootPendingCommand += "$isRebootPending = (($sessionManagerProperties -contains 'PendingFileRenameOperations') -or ($sessionManagerProperties -contains 'PendingFileRenameOperations2') -or $isRebootPending);"
		defaultRebootPendingCommand += "$isRebootPending = (($activeComputerName -ne $pendingComputerName) -or $isRebootPending);"
		defaultRebootPendingCommand += "$isRebootPending = (($systemNetLogonProperties -contains 'AvoidSpnSet') -or ($systemNetLogonProperties -contains 'JoinDomain') -or $isRebootPending);"
		defaultRebootPendingCommand += "exit $isRebootPending;"
		defaultRebootValidateCommand = `powershell -Command "exit 0;" -ExecutionPolicy "Bypass"`
		defaultRemoteEnvVarPathFormat = `C:/Windows/Temp/packer-pwsh-variables-%s.ps1`
		defaultRemotePwshAutoUpdateScriptPathFormat = `C:/Windows/Temp/packer-pwsh-installer-%s.ps1`
		defaultRemoteScriptPathFormat = `C:/Windows/Temp/packer-pwsh-script-%s.ps1`
	default:
		packersdk.MultiErrorAppend(e, fmt.Errorf("Unsupported operating system detected: %s.", runtime.GOOS))
	}

	if "" == p.config.ElevatedEnvVarFormat {
		p.config.ElevatedEnvVarFormat = defaultElevatedEnvVarFormat
	}

	if "" == p.config.ElevatedExecuteCommand {
		p.config.ElevatedExecuteCommand = defaultExecuteCommand
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

	if "" == p.config.PwshAutoUpdateCommand {
		p.config.PwshAutoUpdateCommand = defaultPwshAutoUpdateCommand
	}

	if p.config.PwshAutoUpdateIsEnabled {
		if ("" == p.config.PwshInstallerUri) && ("" != defaultPwshInstallerUri) {
			p.config.PwshInstallerUri = defaultPwshInstallerUri
		}

		if "" == p.config.PwshAutoUpdateScript {
			p.config.PwshAutoUpdateScript = fmt.Sprintf(defaultPwshAutoUpdateScriptFormat, p.config.PwshInstallerUri)
		}
	}

	if "" == p.config.RebootCompleteCommand {
		p.config.RebootCompleteCommand = defaultRebootCompleteCommand
	}

	if "" == p.config.RebootInitiateCommand {
		p.config.RebootInitiateCommand = defaultRebootInitiateCommand
	}

	if "" == p.config.RebootPendingCommand {
		p.config.RebootPendingCommand = defaultRebootPendingCommand
	}

	if "" == p.config.RebootValidateCommand {
		p.config.RebootValidateCommand = defaultRebootValidateCommand
	}

	if "" == p.config.RemoteEnvVarPath {
		p.config.RemoteEnvVarPath = fmt.Sprintf(defaultRemoteEnvVarPathFormat, uuid.TimeOrderedUUID())
	}

	if "" == p.config.RemotePath {
		p.config.RemotePath = fmt.Sprintf(defaultRemoteScriptPathFormat, uuid.TimeOrderedUUID())
	}

	if "" == p.config.RemotePwshAutoUpdatePath {
		p.config.RemotePwshAutoUpdatePath = fmt.Sprintf(defaultRemotePwshAutoUpdateScriptPathFormat, uuid.TimeOrderedUUID())
	}

	if nil == p.config.Scripts {
		p.config.Scripts = make([]string, 0)
	}

	if nil == p.config.Vars {
		p.config.Vars = make([]string, 0)
	}

	if ("" != p.config.ElevatedPassword) && ("" == p.config.ElevatedUser) {
		e = packersdk.MultiErrorAppend(e, errors.New("Must supply the 'elevated_user' parameter if 'elevated_password' is provided."))
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
func (p *Provisioner) Provision(context context.Context, ui packersdk.Ui, communicator packersdk.Communicator, generatedData map[string]interface{}) error {
	p.communicator = communicator
	p.config.ctx.Data = generatedData
	p.generatedData = generatedData

	if p.config.PwshAutoUpdateIsEnabled {
		if e := p.updatePwshInstallation(context, ui); nil != e {
			return e
		}
	}

	if scripts, e := p.initializeScriptCollection(); nil != e {
		return e
	} else {
		if e = p.executeScriptCollection(context, scripts, ui); nil != e {
			return e
		} else {
			if p.config.PostProvisionRebootIsEnabled {
				return p.rebootMachine(context, ui)
			} else {
				return nil
			}
		}
	}
}

func (p *Provisioner) executeScriptCollection(context context.Context, scripts []string, ui packersdk.Ui) error {
	remotePath := p.config.RemotePath
	p.generatedData["Path"] = remotePath

	if command, e := interpolate.Render(p.config.ExecuteCommand, &p.config.ctx); nil != e {
		return e
	} else {
		ui.Say(fmt.Sprintf(`Provisioning with pwsh; command template: %s`, command))

		if "" != p.config.ElevatedUser {
			if command, e = guestexec.GenerateElevatedRunner(command, p); nil != e {
				return e
			}
		}

		return p.uploadAndExecuteScripts(command, context, remotePath, scripts, ui)
	}
}
func (p *Provisioner) getInlineScriptFilePath(lines []string) (string, error) {
	if (nil == lines) || (0 == len(lines)) {
		return "", nil
	} else {
		if scriptFileHandle, e := tmp.File("pwsh-provisioner"); nil != e {
			return "", e
		} else {
			writer := bufio.NewWriter(scriptFileHandle)

			for _, command := range lines {
				if _, e := writer.WriteString(command + "\n"); nil != e {
					return "", fmt.Errorf(pwshScriptPreparingErrorFormat, e)
				}
			}

			if e = writer.Flush(); nil != e {
				return "", fmt.Errorf(pwshScriptPreparingErrorFormat, e)
			} else if e = scriptFileHandle.Close(); nil != e {
				return "", fmt.Errorf(pwshScriptPreparingErrorFormat, e)
			} else {
				return scriptFileHandle.Name(), nil
			}
		}
	}
}
func (p *Provisioner) getUploadAndExecuteScriptFunc(command string, remotePath string, scriptFileHandle *os.File, scriptFileInfo *os.FileInfo, ui packersdk.Ui) (fn func(context.Context) error) {
	return func(context context.Context) error {
		if _, e := scriptFileHandle.Seek(0, 0); nil != e {
			return e
		} else if e = p.communicator.Upload(remotePath, scriptFileHandle, scriptFileInfo); nil != e {
			return fmt.Errorf(pwshScriptUploadingErrorFormat, e)
		} else {
			remoteCmd := &packersdk.RemoteCmd{Command: command}

			if e = remoteCmd.RunWithUi(context, p.communicator, ui); nil != e {
				return e
			} else {
				ui.Say(fmt.Sprintf("Provisioning with pwsh; exit code: %d", remoteCmd.ExitStatus()))

				return p.config.ValidExitCode(remoteCmd.ExitStatus())
			}
		}
	}
}
func (p *Provisioner) initializeScriptCollection() ([]string, error) {
	if inlineScriptFilePath, e := p.getInlineScriptFilePath(p.config.Inline); nil != e {
		return nil, e
	} else {
		scripts := make([]string, len(p.config.Scripts))

		if "" != inlineScriptFilePath {
			scripts = append(scripts, inlineScriptFilePath)
		}

		copy(scripts, p.config.Scripts)

		return scripts, nil
	}
}
func (p *Provisioner) rebootMachine(ctx context.Context, ui packersdk.Ui) error {
	ui.Say(fmt.Sprintf("Initiating machine reboot; command: %s", p.config.RebootInitiateCommand))

	remoteCmd := &packersdk.RemoteCmd{Command: p.config.RebootInitiateCommand}

	if e := remoteCmd.RunWithUi(ctx, p.communicator, ui); nil != e {
		return e
	} else {
		exitCode := remoteCmd.ExitStatus()

		if 0 != exitCode {
			return fmt.Errorf("Failed to reboot machine; exit code: %d", exitCode)
		} else {
			ui.Say(fmt.Sprintf("Waiting for machine reboot; command: %s", p.config.RebootCompleteCommand))
			time.Sleep(13 * time.Second)

			for {
				remoteCmd = &packersdk.RemoteCmd{Command: p.config.RebootCompleteCommand}

				if e = remoteCmd.RunWithUi(ctx, p.communicator, ui); nil != e {
					break
				} else { // TODO: Refactor to support arbitrary operating systems...
					exitCode = remoteCmd.ExitStatus()

					if 0 == exitCode {
						remoteCmd = &packersdk.RemoteCmd{Command: `shutdown /a`}
						remoteCmd.RunWithUi(ctx, p.communicator, ui)

						break
					} else if 1 == exitCode {
						break
					} else if (1115 == exitCode) || (1117 == exitCode) || (1190 == exitCode) {
						time.Sleep(13 * time.Second)
					}
				}
			}

			ui.Say(fmt.Sprintf("Validating machine reboot; command: %s", p.config.RebootValidateCommand))

			for {
				remoteCmd = &packersdk.RemoteCmd{Command: p.config.RebootValidateCommand}

				if e = remoteCmd.RunWithUi(ctx, p.communicator, ui); nil != e {
					return e
				} else {
					exitCode = remoteCmd.ExitStatus()

					if 0 == exitCode {
						break
					} else {
						time.Sleep(13 * time.Second)
					}
				}
			}

			ui.Say(fmt.Sprintf("Completed machine reboot; exit code: %d", exitCode))

			return nil
		}
	}
}
func (p *Provisioner) updatePwshInstallation(context context.Context, ui packersdk.Ui) error {
	if "" != p.config.PwshInstallerUri {
		remotePath := p.config.RemotePwshAutoUpdatePath
		p.generatedData["Path"] = remotePath

		if command, e := interpolate.Render(p.config.PwshAutoUpdateCommand, &p.config.ctx); nil != e {
			return e
		} else {
			ui.Say(fmt.Sprintf(`Updating pwsh installation; command template: %s`, command))

			if updateScriptPath, e := p.getInlineScriptFilePath([]string{p.config.PwshAutoUpdateScript}); nil != e {
				return e
			} else {
				return p.uploadAndExecuteScripts(command, context, remotePath, ([]string{updateScriptPath}), ui)
			}
		}
	} else {
		return nil
	}
}
func (p *Provisioner) uploadAndExecuteScripts(command string, context context.Context, remotePath string, scripts []string, ui packersdk.Ui) error {
	for _, path := range scripts {
		if scriptFileInfo, e := os.Stat(path); nil != e {
			return fmt.Errorf(pwshScriptStatingErrorFormat, e)
		} else {
			ui.Say(fmt.Sprintf("Provisioning with pwsh; script path: %s", path))

			if os.IsPathSeparator(remotePath[len(remotePath)-1]) {
				remotePath += filepath.Base(scriptFileInfo.Name())
			}

			if scriptFileHandle, e := os.Open(path); nil != e {
				return fmt.Errorf(pwshScriptOpeningErrorFormat, e)
			} else {
				if e = (retry.Config{
					StartTimeout: defaultStartTimeout,
					Tries:        defaultTries,
				}.Run(
					context,
					p.getUploadAndExecuteScriptFunc(
						command,
						remotePath,
						scriptFileHandle,
						&scriptFileInfo,
						ui,
					),
				)); nil != e {
					return e
				} else {
					if e = scriptFileHandle.Close(); nil != e {
						return fmt.Errorf(pwshScriptClosingErrorFormat, e)
					}

					if e = os.Remove(scriptFileHandle.Name()); nil != e {
						return fmt.Errorf(pwshScriptRemovingErrorFormat, e)
					}

					if p.config.PostScriptExecutionRebootIsEnabled {
						ui.Say("Checking for pending reboot...")

						remoteCmd := &packersdk.RemoteCmd{Command: p.config.RebootPendingCommand}

						if e = remoteCmd.RunWithUi(context, p.communicator, ui); nil != e {
							return e
						} else {
							if 1 == remoteCmd.ExitStatus() {
								if e = p.rebootMachine(context, ui); nil != e {
									return e
								}
							}
						}
					}
				}
			}
		}
	}

	return nil
}
