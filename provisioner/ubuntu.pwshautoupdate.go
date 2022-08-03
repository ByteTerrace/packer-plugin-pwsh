package pwsh

import (
	"text/template"

	_ "embed"
)

//go:embed ubuntu.pwshautoupdate.sh
var ubuntuPwshAutoUpdateTemplateSh string
var ubuntuPwshAutoUpdateTemplate = template.Must(template.New("UbuntuPwshAutoUpdate").Parse(ubuntuPwshAutoUpdateTemplateSh))
