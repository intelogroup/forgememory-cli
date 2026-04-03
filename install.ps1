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

# Find exe
$EXE = Get-ChildItem -Path $INSTALL_DIR -Filter "*.exe" | Select-Object -First 1
if ($EXE) {
    # Create shim
    $SHIM = "$env:LOCALAPPDATA\Microsoft\WindowsApps\forge.exe"
    Copy-Item $EXE.FullName $SHIM -Force
    
    Write-Host ""
    Write-Host "Done! Run: forge --help"
} else {
    Write-Host "Error: Binary not found" -ForegroundColor Red
}

Remove-Item $TMP -Force -ErrorAction SilentlyContinue
