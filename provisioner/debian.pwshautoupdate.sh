#!/bin/bash

wget --output-document "/tmp/packer-pwsh-installer.deb" "{{.Uri}}"
dpkg -i "/tmp/packer-pwsh-installer.deb"
