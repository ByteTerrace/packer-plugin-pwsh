package pwsh

import (
	"text/template"

	_ "embed"
)

//go:embed windows.rebootpending.ps1
var windowsRebootPendingTemplatePs1 string
var windowsRebootPendingTemplate = template.Must(template.New("WindowsRebootPending").Parse(windowsRebootPendingTemplatePs1))
