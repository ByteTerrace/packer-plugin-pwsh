package pwsh

import (
	"text/template"

	_ "embed"
)

type windowsPwshAutoUpdateOptions struct {
	Uri string
}

//go:embed windows.pwshautoupdate.ps1
var windowsPwshAutoUpdateTemplatePs1 string
var windowsPwshAutoUpdateTemplate = template.Must(template.New("WindowsPwshAutoUpdate").Parse(windowsPwshAutoUpdateTemplatePs1))
