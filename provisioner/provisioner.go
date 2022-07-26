//go:generate packer-sdc mapstructure-to-hcl2 -type Config

package pwsh

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
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

	ElevatedEnvVarFormat         string `mapstructure:"elevated_env_var_format"`
	ElevatedExecuteCommand       string `mapstructure:"elevated_execute_command"`
	ElevatedPassword             string `mapstructure:"elevated_password"`
	ElevatedUser                 string `mapstructure:"elevated_user"`
	OsType                       string `mapstructure:"os_type"`
	PwshAutoUpdateCommand        string `mapstructure:"pwsh_autoupdate_command"`
	PwshAutoUpdateExecuteCommand string `mapstructure:"pwsh_autoupdate_execute_command"`
	PwshAutoUpdateIsEnabled      bool   `mapstructure:"pwsh_autoupdate_is_enabled"`
	RebootCompleteCommand        string `mapstructure:"reboot_complete_command"`
	RebootInitiateCommand        string `mapstructure:"reboot_initiate_command"`
	RebootIsEnabled              bool   `mapstructure:"reboot_is_enabled"`
	RebootPendingCommand         string `mapstructure:"reboot_pending_command"`
	RebootProgressCommand        string `mapstructure:"reboot_progress_command"`
	RebootValidateCommand        string `mapstructure:"reboot_validate_command"`
	RemoteEnvVarPath             string `mapstructure:"remote_env_var_path"`
	RemotePwshAutoUpdatePath     string `mapstructure:"remote_pwsh_autoupdate_path"`

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
func (p *Provisioner) ElevatedExecuteCommand() string {
	return fmt.Sprintf(p.config.ElevatedExecuteCommand, p.config.ExecuteCommand)
}
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
		defaultElevatedUser := p.config.ElevatedUser

		if "" == defaultElevatedUser {
			defaultElevatedUser = "packer"
		}

		defaultElevatedExecuteCommand := fmt.Sprintf(`echo "%s" | sudo -S sh -e -c '%%s'`, defaultElevatedUser)
		defaultExecuteCommand := `chmod +x {{.Path}} && pwsh -ExecutionPolicy "Bypass" -NoLogo -NonInteractive -NoProfile -Command "`
		defaultExecuteCommand += `if (Test-Path variable:global:ErrorActionPreference) { Set-Variable -Name variable:global:ErrorActionPreference -Value ([Management.Automation.ActionPreference]::Stop); } `
		defaultExecuteCommand += `if (Test-Path variable:global:ProgressPreference) { Set-Variable -Name variable:global:ProgressPreference -Value ([Management.Automation.ActionPreference]::SilentlyContinue); } `
		defaultExecuteCommand += `&'{{.Path}}'; exit $LastExitCode;"`
		defaultPwshAutoUpdateExecuteCommand := "chmod +x {{.Path}} && {{.Path}}"
		defaultPwshAutoUpdateScriptExtension := `sh`
		defaultRebootCompleteCommand := ""
		defaultRebootInitiateCommand := ""
		defaultRebootProgressCommand := ""
		defaultRebootValidateCommand := `pwsh -ExecutionPolicy "Bypass" -NoLogo -NonInteractive -NoProfile -Command "exit 0;"`
		defaultRemotePathFormat := `%s/packer-pwsh-%s-%%s.%s`
		defaultRemoteScriptDirectoryPath := `/tmp`

		var defaultPwshAutoUpdateTemplate *template.Template
		var defaultRebootPendingTemplate *template.Template

		p.config.OsType = strings.ToLower(p.config.OsType)

		switch p.config.OsType {
		case "debian":
			defaultPwshAutoUpdateTemplate = debianPwshAutoUpdateTemplate

			break
		case "ubuntu":
			defaultPwshAutoUpdateTemplate = ubuntuPwshAutoUpdateTemplate

			break
		case "windows":
			defaultElevatedExecuteCommand = `%s`
			defaultExecuteCommand = `FOR /F "tokens=* USEBACKQ" %F IN (` + "`where pwsh /R \"%PROGRAMFILES%\\PowerShell\" ^2^>nul ^|^| where powershell`" + `) DO ("%F" -ExecutionPolicy "Bypass" -NoLogo -NonInteractive -NoProfile -Command "`
			defaultExecuteCommand += `if (Test-Path variable:global:ErrorActionPreference) { Set-Variable -Name variable:global:ErrorActionPreference -Value ([Management.Automation.ActionPreference]::Stop); } `
			defaultExecuteCommand += `if (Test-Path variable:global:ProgressPreference) { Set-Variable -Name variable:global:ProgressPreference -Value ([Management.Automation.ActionPreference]::SilentlyContinue); } `
			defaultExecuteCommand += `&'{{.Path}}'; exit $LastExitCode;")`
			defaultPwshAutoUpdateExecuteCommand = defaultExecuteCommand
			defaultPwshAutoUpdateScriptExtension = `ps1`
			defaultPwshAutoUpdateTemplate = windowsPwshAutoUpdateTemplate
			defaultRebootCompleteCommand = `shutdown /a`
			defaultRebootInitiateCommand = `shutdown /r /f /t 0 /c "packer reboot"`
			defaultRebootPendingTemplate = windowsRebootPendingTemplate
			defaultRebootProgressCommand = `shutdown /r /f /t 60 /c "packer reboot test"`
			defaultRemoteScriptDirectoryPath = `C:/Windows/Temp`

			break
		default:
			defaultPwshAutoUpdateTemplate = nil
			defaultRebootPendingTemplate = nil

			break
		}

		var formatRemotePath = func(extension string, suffix string) string {
			return fmt.Sprintf(defaultRemotePathFormat, defaultRemoteScriptDirectoryPath, suffix, extension)
		}

		if "" == p.config.ElevatedExecuteCommand {
			p.config.ElevatedExecuteCommand = defaultElevatedExecuteCommand
		}

		if "" == p.config.ExecuteCommand {
			p.config.ExecuteCommand = defaultExecuteCommand
		}

		if (nil != p.config.Inline) && (0 == len(p.config.Inline)) {
			p.config.Inline = nil
		}

		if ("" == p.config.PwshAutoUpdateCommand) && (nil != defaultPwshAutoUpdateTemplate) {
			var buffer bytes.Buffer

			if err := defaultPwshAutoUpdateTemplate.Execute(&buffer, nil); nil != e {
				e = packersdk.MultiErrorAppend(e, err)
			} else {
				p.config.PwshAutoUpdateCommand = strings.ReplaceAll(strings.ReplaceAll(string(buffer.Bytes()), "\r\n", "\n"), "\r", "\n")
			}
		}

		if "" == p.config.PwshAutoUpdateExecuteCommand {
			p.config.PwshAutoUpdateExecuteCommand = defaultPwshAutoUpdateExecuteCommand
		}

		if "" == p.config.RebootCompleteCommand {
			p.config.RebootCompleteCommand = defaultRebootCompleteCommand
		}

		if "" == p.config.RebootInitiateCommand {
			p.config.RebootInitiateCommand = defaultRebootInitiateCommand
		}

		if ("" == p.config.RebootPendingCommand) && (nil != defaultRebootPendingTemplate) {
			var buffer bytes.Buffer

			if err := defaultRebootPendingTemplate.Execute(&buffer, nil); nil != e {
				e = packersdk.MultiErrorAppend(e, err)
			} else {
				p.config.RebootPendingCommand = strings.ReplaceAll(strings.ReplaceAll(string(buffer.Bytes()), "\r\n", "\n"), "\r", "\n")
			}
		}

		if "" == p.config.RebootProgressCommand {
			p.config.RebootProgressCommand = defaultRebootProgressCommand
		}

		if "" == p.config.RebootValidateCommand {
			p.config.RebootValidateCommand = defaultRebootValidateCommand
		}

		if "" == p.config.RemoteEnvVarPath {
			p.config.RemoteEnvVarPath = fmt.Sprintf(formatRemotePath("ps1", "variables"), uuid.TimeOrderedUUID())
		}

		if "" == p.config.RemotePath {
			p.config.RemotePath = fmt.Sprintf(formatRemotePath("ps1", "script"), uuid.TimeOrderedUUID())
		}

		if "" == p.config.RemotePwshAutoUpdatePath {
			p.config.RemotePwshAutoUpdatePath = fmt.Sprintf(formatRemotePath(defaultPwshAutoUpdateScriptExtension, "installer"), uuid.TimeOrderedUUID())
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

	for _, scriptPath := range scriptPaths {
		ui.Say(fmt.Sprintf("Provisioning with pwsh; script path: %s", scriptPath))

		if exitCode, e := p.uploadAndExecuteScript(context, remotePath, scriptPath, ui); nil != e {
			return e
		} else {
			ui.Say(fmt.Sprintf("Provisioning with pwsh; exit code: %d", exitCode))

			if p.config.ValidExitCode(exitCode); nil != e {
				return e
			} else {
				if p.config.RebootIsEnabled {
					ui.Say("Checking for pending reboot...")

					if rebootScriptPath, e := p.getInlineScriptFilePath([]string{p.config.RebootPendingCommand}); nil != e {
						return e
					} else {
						if exitCode, e = p.uploadAndExecuteScript(context, remotePath, rebootScriptPath, ui); nil != e {
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
	}

	return nil
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

	if updateScriptPath, e := p.getInlineScriptFilePath([]string{p.config.PwshAutoUpdateCommand}); nil != e {
		return e
	} else {
		originalExecuteCommand := p.config.ExecuteCommand
		p.config.ExecuteCommand = p.config.PwshAutoUpdateExecuteCommand
		_, e = p.uploadAndExecuteScript(context, remotePath, updateScriptPath, ui)
		p.config.ExecuteCommand = originalExecuteCommand

		return e
	}
}
func (p *Provisioner) uploadAndExecuteScript(ctx context.Context, remotePath string, scriptPath string, ui packersdk.Ui) (int, error) {
	exitCode := -1

	var command string

	if "" != p.config.ElevatedUser {
		command = p.ElevatedExecuteCommand()
	} else {
		command = p.config.ExecuteCommand
	}

	if command, e := interpolate.Render(command, &p.config.ctx); nil != e {
		return exitCode, e
	} else {
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
							if ("windows" == p.config.OsType) && ("" != p.config.ElevatedUser) {
								if command, e = guestexec.GenerateElevatedRunner(command, p); nil != e {
									return e
								}
							}

							remoteCmd := &packersdk.RemoteCmd{Command: command}

							if e = remoteCmd.RunWithUi(ctx, p.communicator, ui); nil != e {
								return e
							} else {
								exitCode = remoteCmd.ExitStatus()

								return nil
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
}
