param(
    [Parameter(Mandatory)][ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')][string]$Tag,
    [Parameter(Mandatory)][string]$InputPath,
    [Parameter(Mandatory)][ValidatePattern('^[a-fA-F0-9]{64}$')][string]$InputSha256,
    [string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$version = $Tag.Substring(1)
foreach ($part in $version.Split('.')) { if ([long]$part -gt 65535) { throw 'MSIX version component exceeds 65535.' } }
$inputFile = (Resolve-Path -LiteralPath $InputPath).Path
if ((Get-FileHash -LiteralPath $inputFile).Hash -ine $InputSha256) { throw 'Release input checksum mismatch.' }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $repoRoot "dist/store/$Tag" }
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
$out = (Resolve-Path -LiteralPath $OutputDirectory).Path
$package = Join-Path $out "dbterm_${version}.0_x64.msix"
if (Test-Path -LiteralPath $package) { throw 'Package exists; choose a fresh output directory.' }
$stage = Join-Path $out ('stage-' + [guid]::NewGuid().ToString('N'))
if ([version]$version -lt [version]'0.11.1') { throw 'Store requires v0.11.1 or later with Store lifecycle guards.' }
New-Item -ItemType Directory -Path $stage | Out-Null
Copy-Item -LiteralPath $inputFile -Destination (Join-Path $stage 'dbterm.exe')
$reported = & (Join-Path $stage 'dbterm.exe') --version | Out-String
if ($LASTEXITCODE -ne 0 -or $reported -notmatch ('(?<![\w.])' + [regex]::Escape($Tag) + '(?![\w.-])')) { throw 'Executable release version mismatch.' }
Copy-Item -LiteralPath (Join-Path $repoRoot 'LICENSE') -Destination $stage
[xml]$manifest = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'AppxManifest.xml') -Raw
$manifest.Package.Identity.Version = "$version.0"
$manifest.Save((Join-Path $stage 'AppxManifest.xml'))
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'Assets') | Out-Null
Add-Type -AssemblyName System.Drawing
$logoPath = Join-Path $repoRoot 'site/public/favicon-512.png'
$logo = [System.Drawing.Image]::FromFile($logoPath)
try {
  foreach ($asset in @(@('StoreLogo',50),@('Square44x44Logo',44),@('Square150x150Logo',150),@('ListingLogo',300),@('BoxArt',1080))) {
    $size = [int]$asset[1]
    $bitmap = [System.Drawing.Bitmap]::new($size,$size)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    try {
      $graphics.Clear([System.Drawing.Color]::Transparent)
      $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
      $graphics.DrawImage($logo,0,0,$size,$size)
      $bitmap.Save((Join-Path $stage ('Assets/'+$asset[0]+'.png')),[System.Drawing.Imaging.ImageFormat]::Png)
    } finally { $graphics.Dispose(); $bitmap.Dispose() }
  }
} finally { $logo.Dispose() }
$sdkRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits/10/bin'
$sdk = Get-ChildItem -LiteralPath $sdkRoot -Directory | Where-Object { $_.Name -match '^10\.0\.\d+\.0$' } | Sort-Object { [version]$_.Name } -Descending | Where-Object { Test-Path (Join-Path $_.FullName 'x64/makeappx.exe') } | Select-Object -First 1
if (-not $sdk) { throw 'Windows SDK MakeAppx is required.' }
& (Join-Path $sdk.FullName 'x64/makeappx.exe') pack /d $stage /p $package
if ($LASTEXITCODE -ne 0) { throw 'MakeAppx failed.' }
[ordered]@{tag=$Tag; packageVersion="$version.0"; inputSha256=$InputSha256.ToLowerInvariant(); packageSha256=(Get-FileHash $package).Hash.ToLowerInvariant(); recipeCommit=(& git -C $repoRoot rev-parse HEAD); runtimeAcceptance='pending'; package=$package; stage=$stage} | ConvertTo-Json | Set-Content (Join-Path $out 'build-receipt.json') -Encoding utf8
Write-Output "Package: $package"
