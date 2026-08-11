<#
.SYNOPSIS
    koyori-ide Windows MSI ????????WiX Toolset v3??

.DESCRIPTION
    ?? build/scripts/build-windows.ps1 ??? bin/koyori-ide.exe?
    ?? WiX?candle.exe + light.exe??? MSI ????

    ????? bin/??
      koyori-ide-v<version>-windows-<arch>.msi

.PARAMETER Arch
    ?????amd64????? arm64?arm64 ?? WiX 3.14+??

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File build/scripts/build-msi.ps1
    powershell -ExecutionPolicy Bypass -File build/scripts/build-msi.ps1 -Arch amd64
#>

[CmdletBinding()]
param(
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64"
)

$ErrorActionPreference = "Stop"

# ============================================================================
# ?????
# ============================================================================
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RootDir = (Resolve-Path (Join-Path $ScriptDir "..\..")).Path
Set-Location $RootDir

$AppName = "koyori-ide"
$BinDir = Join-Path $RootDir "bin"

# ?????????? release ??????
$VersionFile = Join-Path $RootDir "VERSION"
if (-not (Test-Path $VersionFile)) {
    throw "VERSION file not found: $VersionFile"
}
$Version = (Get-Content $VersionFile -Raw).Trim()
if ($Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
    throw "VERSION contains an invalid SemVer value: $Version"
}
# MSI ProductVersion ??? X.Y.Z?????????
$MsiVersion = ($Version -split '-')[0]

$ExePath = Join-Path $BinDir "$AppName.exe"
if (-not (Test-Path $ExePath)) {
    throw "Built executable not found: $ExePath (run build/scripts/build-windows.ps1 first)"
}

# ============================================================================
# ?? WiX ???candle / light?
# ============================================================================
function Find-WiXTool {
    param([string]$Name)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $candidates = @()
    $candidates += Get-ChildItem "C:\Program Files (x86)\WiX Toolset*\bin\$Name.exe" -ErrorAction SilentlyContinue
    $candidates += Get-ChildItem "C:\Program Files\WiX Toolset*\bin\$Name.exe" -ErrorAction SilentlyContinue
    if ($env:ChocolateyInstall) {
        $candidates += Get-ChildItem (Join-Path $env:ChocolateyInstall "lib\wixtoolset\**\$Name.exe") -ErrorAction SilentlyContinue
    }
    $tool = $candidates | Sort-Object FullName -Descending | Select-Object -First 1
    if (-not $tool) {
        throw "$Name.exe not found. Install WiX Toolset v3: choco install wixtoolset -y"
    }
    return $tool.FullName
}

$Candle = Find-WiXTool "candle"
$Light = Find-WiXTool "light"
Write-Host "[INFO]  candle: $Candle"
Write-Host "[INFO]  light:  $Light"

# ============================================================================
# ?? MSI
# ============================================================================
$WxsPath = Join-Path $RootDir "build\windows\msi\$AppName.wxs"
if (-not (Test-Path $WxsPath)) {
    throw "WiX source not found: $WxsPath"
}
$Icon = Join-Path $RootDir "build\windows\icon.ico"
$UpgradeCode = "594D99DA-3AA0-487C-B791-5827E8C91D69"

$ObjDir = Join-Path $BinDir "wix-obj"
New-Item -ItemType Directory -Force -Path $ObjDir | Out-Null
$WixObj = Join-Path $ObjDir "$AppName.wixobj"
$MsiPath = Join-Path $BinDir "$AppName-v$Version-windows-$Arch.msi"

# -arch ???amd64 -> x64?WiX 3.11+??arm64 ?? WiX 3.14+
$WixArch = if ($Arch -eq "amd64") { "x64" } else { "arm64" }

Write-Host "[INFO]  ?? MSI: $MsiPath"
& $Candle -nologo -arch $WixArch `
    -dVersion=$MsiVersion `
    -dAppExe=$ExePath `
    -dIcon=$Icon `
    -dUpgradeCode=$UpgradeCode `
    -out $WixObj `
    $WxsPath
if ($LASTEXITCODE -ne 0) {
    throw "candle (WiX compiler) failed with exit code $LASTEXITCODE"
}

& $Light -nologo -spdb -out $MsiPath $WixObj
if ($LASTEXITCODE -ne 0) {
    throw "light (WiX linker) failed with exit code $LASTEXITCODE"
}

if (-not (Test-Path $MsiPath)) {
    throw "MSI was not produced: $MsiPath"
}
$SizeMb = [math]::Round((Get-Item $MsiPath).Length / 1MB, 1)
Write-Host "[OK]    MSI ???: $MsiPath ($SizeMb MB)"
Write-Host "[INFO]  ??:       msiexec /i $MsiPath"
Write-Host "[INFO]  ????:   msiexec /i $MsiPath /qn"
