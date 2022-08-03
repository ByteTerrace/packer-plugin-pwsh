#!/bin/bash
apt update && apt install -y curl gnupg apt-transport-https
curl 'https://packages.microsoft.com/keys/microsoft.asc' | apt-key add -
sh -c 'echo "deb [arch=amd64] https://packages.microsoft.com/repos/microsoft-debian-bullseye-prod bullseye main" > /etc/apt/sources.list.d/microsoft.list'
apt update && apt install -y powershell
