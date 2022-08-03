#!/bin/bash
wget --output-document "/tmp/packer-pwsh-installer.deb" "{{.Uri}}" --output-file /dev/null
dpkg -i "/tmp/packer-pwsh-installer.deb"
