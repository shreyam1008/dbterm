#!/usr/bin/env pwsh
<#
.SYNOPSIS
Installs dbterm on Windows without requiring Go.

.DESCRIPTION
Usage:
  irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex

Optional environment overrides:
  $env:DBTERM_REPO="owner/repo"
  $env:DBTERM_VERSION="latest"  # or v1.2.3
  $env:DBTERM_INSTALL_DIR="C:\path\to\bin"
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo = if ($env:DBTERM_REPO) { $env:DBTERM_REPO } else { "shreyam1008/dbterm" }
$Version = if ($env:DBTERM_VERSION) { $env:DBTERM_VERSION } else { "latest" }
$InstallDir = if ($env:DBTERM_INSTALL_DIR) { $env:DBTERM_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "dbterm\bin" }

$binaryName = "dbterm"
$archName = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()

switch ($archName) {
	"X64" { $arch = "amd64" }
	"Arm64" { $arch = "arm64" }
	default { throw "Unsupported architecture: $archName" }
}

$assetName = "$binaryName-windows-$arch.exe"

if ($Version -eq "latest") {
	$baseUrl = "https://github.com/$Repo/releases/latest/download"
} else {
	if (-not $Version.StartsWith("v")) {
		$Version = "v$Version"
	}
	$baseUrl = "https://github.com/$Repo/releases/download/$Version"
}

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("dbterm-install-" + [Guid]::NewGuid().ToString("N"))
$tmpBin = Join-Path $tmpDir $assetName
$tmpChecksums = Join-Path $tmpDir "checksums.txt"
$targetPath = Join-Path $InstallDir "$binaryName.exe"

New-Item -ItemType Directory -Path $tmpDir | Out-Null

try {
	Write-Host "Installing dbterm ($arch)..." -ForegroundColor Cyan
	Invoke-WebRequest -Uri "$baseUrl/$assetName" -OutFile $tmpBin

	try {
		Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile $tmpChecksums
	} catch {
		Write-Host "Could not download checksums; continuing without verification." -ForegroundColor Yellow
	}

	if (Test-Path $tmpChecksums) {
		$expected = $null

		foreach ($line in Get-Content $tmpChecksums) {
			$parts = $line -split "\s+" | Where-Object { $_ -ne "" }
			if ($parts.Count -ge 2 -and $parts[1] -eq $assetName) {
				$expected = $parts[0].ToLowerInvariant()
				break
			}
		}

		if ($expected) {
			$actual = (Get-FileHash -Path $tmpBin -Algorithm SHA256).Hash.ToLowerInvariant()
			if ($expected -ne $actual) {
				throw "Checksum verification failed for $assetName"
			}
			Write-Host "Checksum verified." -ForegroundColor Cyan
		} else {
			Write-Host "No checksum entry for $assetName; skipping verification." -ForegroundColor Yellow
		}
	}

	New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
	Move-Item -Path $tmpBin -Destination $targetPath -Force

	$currentSessionPaths = $env:Path -split ";" | Where-Object { $_ -ne "" }
	if (-not ($currentSessionPaths -contains $InstallDir)) {
		$env:Path = "$InstallDir;$env:Path"
	}

	$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
	$userPaths = @()
	if (-not [string]::IsNullOrWhiteSpace($userPath)) {
		$userPaths = $userPath -split ";" | Where-Object { $_ -ne "" }
	}

	if (-not ($userPaths -contains $InstallDir)) {
		$newUserPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
		[Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
		Write-Host "Added $InstallDir to user PATH." -ForegroundColor Cyan
	}

	& $targetPath --version | Out-Null
	Write-Host "Success! dbterm installed at $targetPath" -ForegroundColor Green
	Write-Host "Run: dbterm"
	Write-Host "If dbterm is not found in this terminal, open a new terminal window."
} finally {
	Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
