package pwsh

import (
	"text/template"

	_ "embed"
)

//go:embed debian.pwshautoupdate.sh
var debianPwshAutoUpdateTemplateSh string
var debianPwshAutoUpdateTemplate = template.Must(template.New("DebianPwshAutoUpdate").Parse(debianPwshAutoUpdateTemplateSh))
