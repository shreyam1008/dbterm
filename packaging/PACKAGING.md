# dbterm — Packaging Guide

Publisher: Shreyam Adhikari (shreyam1008@gmail.com)
Version: 0.4.1

---

## Files in this directory

| Path | Purpose |
| --- | --- |
| `homebrew/dbterm.rb` | Homebrew tap formula |
| `aur/PKGBUILD` | AUR `dbterm-bin` package |
| `scoop/dbterm.json` | Scoop bucket manifest |
| `winget/manifests/s/ShreyamAdhikari/dbterm/0.4.1/` | WinGet manifests |

---

## Step 0: Prepare release binaries on GitHub

Every packaging system below downloads binaries from GitHub Releases.
Before submitting anywhere, make sure the release tag exists with these artifacts:

```
dbterm-linux-amd64
dbterm-linux-arm64
dbterm-darwin-amd64
dbterm-darwin-arm64
dbterm-windows-amd64.exe
dbterm-windows-arm64.exe
```

Run: `make release` to build all of these into `dist/`.

---

## 1. Homebrew tap (do this first — zero review, instant)

### Create the tap repo

```bash
# On GitHub: create repo named "homebrew-tap"
# Full name: github.com/shreyam1008/homebrew-tap
```

### Add the formula

```bash
git clone https://github.com/shreyam1008/homebrew-tap
cd homebrew-tap
mkdir -p Formula
cp /home/shre/Desktop/me/dbterm/packaging/homebrew/dbterm.rb Formula/dbterm.rb

# Fill in the real sha256 of the source tarball:
curl -sL https://github.com/shreyam1008/dbterm/archive/refs/tags/v0.4.1.tar.gz | sha256sum
# Paste result into dbterm.rb sha256 field

git add Formula/dbterm.rb
git commit -m "Add dbterm v0.4.1"
git push
```

### Users install with

```bash
brew tap shreyam1008/tap
brew install shreyam1008/tap/dbterm
```

---

## 2. AUR

### Get the binary sha256 values

```bash
curl -sL https://github.com/shreyam1008/dbterm/releases/download/v0.4.1/dbterm-linux-amd64 | sha256sum
curl -sL https://github.com/shreyam1008/dbterm/releases/download/v0.4.1/dbterm-linux-arm64 | sha256sum
```

### Publish

```bash
# 1. Create AUR account at https://aur.archlinux.org
# 2. Add SSH key in AUR account settings
# 3. Clone the empty AUR repo
git clone ssh://aur@aur.archlinux.org/dbterm-bin.git
cd dbterm-bin

# 4. Copy and update PKGBUILD
cp /home/shre/Desktop/me/dbterm/packaging/aur/PKGBUILD .
# Edit: replace sha256sums_x86_64=('SKIP') with actual sha256

# 5. Test
makepkg -si

# 6. Generate .SRCINFO and push
makepkg --printsrcinfo > .SRCINFO
git add PKGBUILD .SRCINFO
git commit -m "Initial release v0.4.1"
git push
```

---

## 3. Scoop (Windows, developer users)

Use one shared Scoop bucket repo for all your apps: `shreyam1008/scoop-bucket`.

That repo contains one JSON manifest per app:

```text
scoop-bucket/
  bucket/
    dbterm.json
    markpad.json
    gobarrygo.json
```

Users add the bucket once, then install any app from it.

### Create the bucket repo

```bash
# On GitHub: create repo named "scoop-bucket"
# Full name: github.com/shreyam1008/scoop-bucket
```

### Get the binary sha256 values

```powershell
# Windows:
certutil -hashfile dbterm-windows-amd64.exe SHA256
certutil -hashfile dbterm-windows-arm64.exe SHA256
```

```bash
# Linux/macOS:
sha256sum dbterm-windows-amd64.exe
sha256sum dbterm-windows-arm64.exe
```

### Add the manifest

```bash
git clone https://github.com/shreyam1008/scoop-bucket
cd scoop-bucket
mkdir -p bucket
cp /home/shre/Desktop/me/dbterm/packaging/scoop/dbterm.json bucket/dbterm.json
# Edit: replace TODO hash values with actual sha256 values
git add bucket/dbterm.json
git commit -m "Add dbterm v0.4.1"
git push
```

### Users install with

```powershell
scoop bucket add shreyam1008 https://github.com/shreyam1008/scoop-bucket
scoop install dbterm
```

---

## 4. WinGet

### Prerequisites

- The `dbterm-windows-amd64.exe` must be on GitHub Releases at a permanent URL.
- Get sha256:

```powershell
certutil -hashfile dbterm-windows-amd64.exe SHA256
```

### Steps

1. Fork https://github.com/microsoft/winget-pkgs
2. Create the path: `manifests/s/ShreyamAdhikari/dbterm/0.4.1/`
3. Add the three YAML files from `packaging/winget/manifests/s/ShreyamAdhikari/dbterm/0.4.1/`
4. Edit: replace placeholder `InstallerSha256` values with real sha256 values
5. Validate locally:

```powershell
winget validate manifests/s/ShreyamAdhikari/dbterm/0.4.1/
```

6. Submit PR to winget-pkgs

### After approval, users install with

```powershell
winget install ShreyamAdhikari.dbterm
```

---

## Release checklist

- [ ] `go test ./...` passes
- [ ] `make release` builds all platform binaries
- [ ] GitHub Release tag created with all binaries attached
- [ ] Homebrew formula sha256 updated
- [ ] AUR PKGBUILD sha256 updated and .SRCINFO regenerated
- [ ] Scoop manifest hash updated
- [ ] WinGet installer sha256 updated
- [ ] `versions.txt` updated with new version as first entry
