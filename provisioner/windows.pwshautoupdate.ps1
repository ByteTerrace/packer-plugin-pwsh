$exitCode = -1;

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12;
    $tempFilePath = ('{0}packer-pwsh-installer.msi' -f [IO.Path]::GetTempPath());
    Invoke-WebRequest -OutFile $tempFilePath -Uri '{{.Uri}}';
    $exitCode = (Start-Process -ArgumentList @('/i', $tempFilePath, '/norestart', '/qn') -FilePath 'msiexec.exe' -PassThru -Wait).ExitCode;
}
finally {
    exit $exitCode;
}
