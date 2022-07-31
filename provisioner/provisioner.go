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

	ElevatedEnvVarFormat       string `mapstructure:"elevated_env_var_format"`
	ElevatedExecuteCommand     string `mapstructure:"elevated_execute_command"`
	ElevatedPassword           string `mapstructure:"elevated_password"`
	ElevatedUser               string `mapstructure:"elevated_user"`
	PwshAutoUpdateCommand      string `mapstructure:"pwsh_autoupdate_command"`
	PwshAutoUpdateInstallerUri string `mapstructure:"pwsh_autoupdate_installer_uri"`
	PwshAutoUpdateIsEnabled    bool   `mapstructure:"pwsh_autoupdate_is_enabled"`
	RebootCompleteCommand      string `mapstructure:"reboot_complete_command"`
	RebootInitiateCommand      string `mapstructure:"reboot_initiate_command"`
	RebootPendingCommand       string `mapstructure:"reboot_pending_command"`
	RebootProgressCommand      string `mapstructure:"reboot_progress_command"`
	RebootValidateCommand      string `mapstructure:"reboot_validate_command"`
	RemoteEnvVarPath           string `mapstructure:"remote_env_var_path"`
	RemotePwshAutoUpdatePath   string `mapstructure:"remote_pwsh_autoupdate_path"`

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
func (p *Provisioner) ElevatedPassword() string {
	elevatedPassword, _ := interpolate.Render(p.config.ElevatedPassword, &p.config.ctx)

	return elevatedPassword
}
func (p *Provisioner) ElevatedUser() string {
	return p.config.ElevatedUser
}
func (p *Provisioner) Prepare(raws ...interface{}) error {
	if e := config.Decode(
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
	); nil != e {
		return e
	} else {
		var defaultElevatedEnvVarFormat string
		var defaultEnvVarFormat string
		var defaultExecuteCommand string
		var defaultPwshAutoUpdateCommandFormat string
		var defaultPwshAutoUpdateInstallerUri string
		var defaultRebootCompleteCommand string
		var defaultRebootInitiateCommand string
		var defaultRebootProgressCommand string
		var defaultRebootPendingCommand string
		var defaultRebootValidateCommand string
		var defaultRemoteEnvVarPathFormat string
		var defaultRemotePwshAutoUpdateScriptPathFormat string
		var defaultRemoteScriptPathFormat string

		switch runtime.GOOS {
		case "windows":
			defaultElevatedEnvVarFormat = `${Env:%s}="%s"`
			defaultEnvVarFormat = `{$Env:%s}="%s"`
			defaultExecuteCommand = `FOR /F "tokens=* USEBACKQ" %F IN (` + "`where pwsh /R \"%PROGRAMFILES%\\PowerShell\" ^2^>nul ^|^| where powershell`" + `) DO ("%F" -ExecutionPolicy "Bypass" -NoLogo -NonInteractive -NoProfile -Command "&'{{.Path}}'; exit $LastExitCode;")`
			defaultPwshAutoUpdateCommandFormat = "$ErrorActionPreference = [Management.Automation.ActionPreference]::Stop;\n"
			defaultPwshAutoUpdateCommandFormat += "$exitCode = -1;\n"
			defaultPwshAutoUpdateCommandFormat += "try {\n"
			defaultPwshAutoUpdateCommandFormat += "    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12;\n"
			defaultPwshAutoUpdateCommandFormat += "    $tempFilePath = ('{0}packer-pwsh-installer.msi' -f [IO.Path]::GetTempPath());\n"
			defaultPwshAutoUpdateCommandFormat += "    Invoke-WebRequest -OutFile $tempFilePath -Uri '%s';\n"
			defaultPwshAutoUpdateCommandFormat += "    $exitCode = (Start-Process -ArgumentList @('/i', $tempFilePath, '/norestart', '/qn') -FilePath 'msiexec.exe' -PassThru -Wait).ExitCode;\n"
			defaultPwshAutoUpdateCommandFormat += "}\n"
			defaultPwshAutoUpdateCommandFormat += "finally {\n"
			defaultPwshAutoUpdateCommandFormat += "    exit $exitCode;\n"
			defaultPwshAutoUpdateCommandFormat += "}\n"
			defaultPwshAutoUpdateInstallerUri = "https://github.com/PowerShell/PowerShell/releases/download/v7.2.5/PowerShell-7.2.5-win-x64.msi"
			defaultRebootCompleteCommand = `shutdown /a`
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
			defaultRebootProgressCommand = `shutdown /r /f /t 60 /c "packer reboot test"`
			defaultRebootValidateCommand = `powershell -ExecutionPolicy "Bypass" -NoLogo -NonInteractive -NoProfile -Command "exit 0;"`
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

		if p.config.PwshAutoUpdateIsEnabled {
			if ("" == p.config.PwshAutoUpdateInstallerUri) && ("" != defaultPwshAutoUpdateInstallerUri) {
				p.config.PwshAutoUpdateInstallerUri = defaultPwshAutoUpdateInstallerUri
			}

			if "" == p.config.PwshAutoUpdateCommand {
				p.config.PwshAutoUpdateCommand = fmt.Sprintf(defaultPwshAutoUpdateCommandFormat, p.config.PwshAutoUpdateInstallerUri)
			}
		}

		if "" == p.config.RebootCompleteCommand {
			p.config.RebootCompleteCommand = defaultRebootCompleteCommand
		}

		if "" == p.config.RebootInitiateCommand {
			p.config.RebootInitiateCommand = defaultRebootInitiateCommand
		}

		if "" == p.config.RebootProgressCommand {
			p.config.RebootProgressCommand = defaultRebootProgressCommand
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

	if scriptPaths, e := p.initializeScriptCollection(); nil != e {
		return e
	} else {
		return p.executeScriptCollection(context, scriptPaths, ui)
	}
}

func (p *Provisioner) executeScriptCollection(context context.Context, scriptPaths []string, ui packersdk.Ui) error {
	remotePath := p.config.RemotePath
	p.generatedData["Path"] = remotePath

	if command, e := interpolate.Render(p.config.ExecuteCommand, &p.config.ctx); nil != e {
		return e
	} else {
		ui.Say(fmt.Sprintf(`Provisioning with pwsh; command template: %s`, command))

		for _, scriptPath := range scriptPaths {
			ui.Say(fmt.Sprintf("Provisioning with pwsh; script path: %s", scriptPath))

			if exitCode, e := p.uploadAndExecuteScript(command, context, remotePath, scriptPath, ui); nil != e {
				return e
			} else {
				ui.Say(fmt.Sprintf("Provisioning with pwsh; exit code: %d", exitCode))

				if "" != p.config.RebootPendingCommand {
					ui.Say("Checking for pending reboot...")

					if rebootScriptPath, e := p.getInlineScriptFilePath([]string{p.config.RebootPendingCommand}); nil != e {
						return e
					} else {
						if exitCode, e = p.uploadAndExecuteScript(command, context, remotePath, rebootScriptPath, ui); nil != e {
							return e
						} else if 1 == exitCode {
							if e = p.rebootMachine(context, ui); nil != e {
								return e
							}
						}
					}
				}
			}
		}

		return nil
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
			ui.Say(fmt.Sprintf("Waiting for machine reboot; command: %s", p.config.RebootProgressCommand))

			for {
				time.Sleep(13 * time.Second)

				remoteCmd = &packersdk.RemoteCmd{Command: p.config.RebootProgressCommand}

				if e = remoteCmd.RunWithUi(ctx, p.communicator, ui); nil != e { // TODO: Consider inspecting the error instead of ignoring it.
					break
				} else {
					exitCode = remoteCmd.ExitStatus()

					if 0 == exitCode {
						remoteCmd = &packersdk.RemoteCmd{Command: p.config.RebootCompleteCommand}
						remoteCmd.RunWithUi(ctx, p.communicator, ui)

						break
					} else if 1 == exitCode {
						break
					} else {
						continue // TODO: Consider handling the remaining exitCodes instead of ignoring them.
					}
				}
			}

			ui.Say(fmt.Sprintf("Validating machine reboot; command: %s", p.config.RebootValidateCommand))

			for {
				remoteCmd = &packersdk.RemoteCmd{Command: p.config.RebootValidateCommand}

				if e = remoteCmd.RunWithUi(ctx, p.communicator, ui); nil == e { // TODO: Consider inspecting the error instead of ignoring it.
					exitCode = remoteCmd.ExitStatus()

					if 0 == exitCode {
						break
					}
				}

				time.Sleep(13 * time.Second)
			}

			ui.Say(fmt.Sprintf("Completed machine reboot; exit code: %d", exitCode))

			return nil
		}
	}
}
func (p *Provisioner) updatePwshInstallation(context context.Context, ui packersdk.Ui) error {
	remotePath := p.config.RemotePwshAutoUpdatePath
	p.generatedData["Path"] = remotePath

	if command, e := interpolate.Render(p.config.ExecuteCommand, &p.config.ctx); nil != e {
		return e
	} else {
		ui.Say(fmt.Sprintf(`Updating pwsh installation; command template: %s`, command))

		if updateScriptPath, e := p.getInlineScriptFilePath([]string{p.config.PwshAutoUpdateCommand}); nil != e {
			return e
		} else {
			_, e = p.uploadAndExecuteScript(command, context, remotePath, updateScriptPath, ui)

			return e
		}
	}
}
func (p *Provisioner) uploadAndExecuteScript(command string, ctx context.Context, remotePath string, scriptPath string, ui packersdk.Ui) (int, error) {
	exitCode := -1

	if scriptFileInfo, e := os.Stat(scriptPath); nil != e {
		return exitCode, fmt.Errorf(pwshScriptStatingErrorFormat, e)
	} else {
		if os.IsPathSeparator(remotePath[len(remotePath)-1]) {
			remotePath += filepath.Base(scriptFileInfo.Name())
		}

		if scriptFileHandle, e := os.Open(scriptPath); nil != e {
			return exitCode, fmt.Errorf(pwshScriptOpeningErrorFormat, e)
		} else {
			if e = (retry.Config{
				StartTimeout: defaultStartTimeout,
				Tries:        defaultTries,
			}.Run(
				ctx,
				func(ctx context.Context) error {
					if _, e := scriptFileHandle.Seek(0, 0); nil != e {
						return e
					} else if e = p.communicator.Upload(remotePath, scriptFileHandle, &scriptFileInfo); nil != e {
						return fmt.Errorf(pwshScriptUploadingErrorFormat, e)
					} else {
						if "" != p.config.ElevatedUser {
							if command, e = guestexec.GenerateElevatedRunner(command, p); nil != e {
								return e
							}
						}

						remoteCmd := &packersdk.RemoteCmd{Command: command}

						if e = remoteCmd.RunWithUi(ctx, p.communicator, ui); nil != e {
							return e
						} else {
							exitCode = remoteCmd.ExitStatus()

							return p.config.ValidExitCode(exitCode)
						}
					}
				},
			)); nil != e {
				return exitCode, e
			} else {
				if e = scriptFileHandle.Close(); nil != e {
					return exitCode, fmt.Errorf(pwshScriptClosingErrorFormat, e)
				}

				if e = os.Remove(scriptFileHandle.Name()); nil != e {
					return exitCode, fmt.Errorf(pwshScriptRemovingErrorFormat, e)
				}

				return exitCode, nil
			}
		}
	}
}
