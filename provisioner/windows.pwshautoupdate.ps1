$exitCode = -1;

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12;
    $tempFilePath = ('{0}packer-pwsh-installer.msi' -f [IO.Path]::GetTempPath());
    Invoke-WebRequest -OutFile $tempFilePath -Uri 'https://github.com/PowerShell/PowerShell/releases/download/v7.2.5/PowerShell-7.2.5-win-x64.msi';
    $exitCode = (Start-Process -ArgumentList @('/i', $tempFilePath, '/norestart', '/qn') -FilePath 'msiexec.exe' -PassThru -Wait).ExitCode;
}
finally {
    exit $exitCode;
}
