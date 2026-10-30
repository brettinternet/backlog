# Download and verify bl before replacing the installed binary.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
Set-StrictMode -Version Latest
function Fail($message) { [Console]::Error.WriteLine($message); exit 1 }
switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64' { $arch = 'amd64' }
    'Arm64' { $arch = 'arm64' }
    default { Fail 'Unsupported architecture' }
}
$version = if ($env:BL_VERSION) { $env:BL_VERSION } else { 'latest' }
if ($version -ceq 'latest') { $release = 'latest/download' }
elseif ($version -clike 'v*') { $release = "download/$version" }
else { Fail 'BL_VERSION must be latest or a v-prefixed tag' }
$base = if ($env:BL_RELEASE_BASE_URL) { $env:BL_RELEASE_BASE_URL } else { 'https://github.com/brettinternet/backlog/releases' }
$destination = if ($env:BL_INSTALL_DIR) { $env:BL_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\bl' }
$archive = "bl_windows_$arch.zip"
# Windows PowerShell 5.1 may default to TLS 1.0, which GitHub refuses.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
Add-Type -AssemblyName System.IO.Compression.FileSystem
$scratch = New-Item -ItemType Directory -Path (Join-Path ([IO.Path]::GetTempPath()) "bl-install-$([Guid]::NewGuid())")
$archivePath = Join-Path $scratch $archive
$checksumsPath = Join-Path $scratch 'checksums.txt'
$binary = Join-Path $scratch 'bl.exe'
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$release/$archive" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$release/checksums.txt" -OutFile $checksumsPath
    $entries = @(Get-Content -LiteralPath $checksumsPath | ForEach-Object { , ($_.Trim() -split '\s+') } | Where-Object { $_.Count -ge 2 -and $_[1] -ceq $archive })
    if ($entries.Count -ne 1) { Fail 'Missing or duplicate checksum' }
    $expected = $entries[0][0]
    if ($expected -cnotmatch '^[0-9a-f]{64}$') { Fail 'Invalid checksum' }
    $actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -cne $expected) { Fail 'Checksum mismatch; nothing installed' }
    $zip = [IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        $entry = $zip.GetEntry('bl.exe')
        if (-not $entry) { Fail 'Invalid binary' }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $binary)
    } finally { $zip.Dispose() }
    New-Item -ItemType Directory -Force -Path $destination | Out-Null
    Copy-Item -LiteralPath $binary -Destination (Join-Path $destination 'bl.exe') -Force
} finally {
    # Only remove files created by this process; no recursive variable-path deletion.
    foreach ($file in $archivePath, $checksumsPath, $binary) { Remove-Item -LiteralPath $file -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $scratch
}
Write-Output "Installed $(Join-Path $destination 'bl.exe')"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath -or ($userPath -split ';') -notcontains $destination) {
    Write-Output "Add it to PATH, then open a new terminal:"
    Write-Output "  [Environment]::SetEnvironmentVariable('Path', `"$destination;`" + [Environment]::GetEnvironmentVariable('Path', 'User'), 'User')"
}
