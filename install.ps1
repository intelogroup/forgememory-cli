# Forge Windows Installer (PowerShell)
# Run in PowerShell as Admin:
# iwr https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.ps1 | iex

$REPO = "intelogroup/forgememory-cli"
$VERSION = "latest"
$INSTALL_DIR = "$env:LOCALAPPDATA\Forge\bin"

# Detect arch
$ARCH = if ($env:PROCESSOR_ARCHITECTURE -eq "AMD64") { "amd64" } else { "arm64" }

# Get latest version
if ($VERSION -eq "latest") {
    $VERSION = (Invoke-RestMethod "https://api.github.com/repos/$REPO/releases/latest").tag_name
}

$BASE_URL = "https://github.com/$REPO/releases/download/$VERSION"
$ARCHIVE = "forge-windows-$ARCH.zip"
$URL = "$BASE_URL/$ARCHIVE"

Write-Host "Installing Forge $VERSION to $INSTALL_DIR..."

# Create install dir
New-Item -ItemType Directory -Force -Path $INSTALL_DIR | Out-Null

# Download
$TMP = [System.IO.Path]::GetTempFileName()
Invoke-WebRequest -Uri $URL -OutFile $TMP

# Extract
Expand-Archive -Path $TMP -DestinationPath $INSTALL_DIR -Force

# Find extracted exe and normalize command names
$EXE = Get-ChildItem -Path $INSTALL_DIR -Filter "forge-windows-*.exe" | Select-Object -First 1
if ($EXE) {
    $FORGEMEMO_EXE = Join-Path $INSTALL_DIR "forgememo.exe"
    $FORGE_EXE = Join-Path $INSTALL_DIR "forge.exe"
    Copy-Item $EXE.FullName $FORGEMEMO_EXE -Force
    Copy-Item $EXE.FullName $FORGE_EXE -Force
    Remove-Item $EXE.FullName -Force -ErrorAction SilentlyContinue

    # Create user command shims
    $SHIM_DIR = "$env:LOCALAPPDATA\Microsoft\WindowsApps"
    Copy-Item $FORGEMEMO_EXE (Join-Path $SHIM_DIR "forgememo.exe") -Force
    Copy-Item $FORGE_EXE (Join-Path $SHIM_DIR "forge.exe") -Force

    Write-Host ""
    Write-Host "Done! Run: forgememo --help"
} else {
    Write-Host "Error: Binary not found" -ForegroundColor Red
}

Remove-Item $TMP -Force -ErrorAction SilentlyContinue
