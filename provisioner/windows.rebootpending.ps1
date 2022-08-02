$activeComputerName = (Get-ItemProperty -Name 'ComputerName' -Path 'HKLM:/SYSTEM/CurrentControlSet/Control/ComputerName/ActiveComputerName').ComputerName;
$componentBasedServicingProperties = (Get-Item -Path 'HKLM:/SOFTWARE/Microsoft/Windows/CurrentVersion/Component Based Servicing').Property;
$pendingComputerName = (Get-ItemProperty -Name 'ComputerName' -Path 'HKLM:/SYSTEM/CurrentControlSet/Control/ComputerName/ComputerName').ComputerName;
$sessionManagerProperties = (Get-Item -Path 'HKLM:/System/CurrentControlSet/Control/Session Manager').Property;
$systemNetLogonProperties = (Get-Item -Path 'HKLM:/SYSTEM/CurrentControlSet/Services/Netlogon').Property;
$windowsUpdateProperties = (Get-Item -Path 'HKLM:/SOFTWARE/Microsoft/Windows/CurrentVersion/WindowsUpdate/Auto Update').Property;
$isRebootPending = (($componentBasedServicingProperties -contains 'PackagesPending') -or ($componentBasedServicingProperties -contains 'RebootInProgress') -or ($componentBasedServicingProperties -contains 'RebootPending'));
$isRebootPending = (($windowsUpdateProperties -contains 'PostRebootReporting') -or ($windowsUpdateProperties -contains 'RebootRequired') -or $isRebootPending);
$isRebootPending = (($sessionManagerProperties -contains 'PendingFileRenameOperations') -or ($sessionManagerProperties -contains 'PendingFileRenameOperations2') -or $isRebootPending);
$isRebootPending = (($activeComputerName -ne $pendingComputerName) -or $isRebootPending);
$isRebootPending = (($systemNetLogonProperties -contains 'AvoidSpnSet') -or ($systemNetLogonProperties -contains 'JoinDomain') -or $isRebootPending);
exit $isRebootPending;
