# Publishes the bundled ShopFlow demo_app as a clean, standalone PUBLIC Git repo so it
# can be used as a PatchPilot "Git source" (Settings -> Git source -> Connect / Clone).
#
# It stages demo_app/ (minus build artifacts) into a fresh git repo and pushes it to a
# remote you provide. The GitHub CLI (gh) is NOT required — create the empty public repo
# in the GitHub UI first, then pass its URL with -RemoteUrl.
#
#   # 1) Create an empty public repo on GitHub (no README), e.g.
#   #    https://github.com/<you>/patchpilot-demo-app
#   # 2) Publish:
#   pwsh scripts/publish_demo_repo.ps1 -RemoteUrl https://github.com/<you>/patchpilot-demo-app.git
#
# Use -StageOnly to build the clean repo locally without pushing (prints the path), or
# omit -RemoteUrl to be reminded of the manual steps.

param(
  [string]$RemoteUrl,
  [string]$Branch = "main",
  [string]$CommitMessage = "ShopFlow demo app (seeded bugs for PatchPilot)",
  [switch]$StageOnly
)

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$demo = Join-Path $root "demo_app"
if (-not (Test-Path (Join-Path $demo "main.go"))) {
  throw "demo_app not found at $demo — run this from the PatchPilot repo."
}

# Author identity (reuse the Git source author vars from .env when present).
$cfg = @{}
$envFile = Join-Path $root ".env"
if (Test-Path $envFile) {
  Get-Content $envFile | ForEach-Object {
    if ($_ -match '^\s*([A-Z0-9_]+)\s*=\s*(.*)$') { $cfg[$matches[1]] = $matches[2].Trim() }
  }
}
$authorName  = if ($cfg["GIT_SOURCE_COMMIT_NAME"])  { $cfg["GIT_SOURCE_COMMIT_NAME"] }  else { "PatchPilot" }
$authorEmail = if ($cfg["GIT_SOURCE_COMMIT_EMAIL"]) { $cfg["GIT_SOURCE_COMMIT_EMAIL"] } else { "patchpilot@local" }

# Stage a clean copy: source + tests + Dockerfile, excluding build artifacts.
$exclude = @("web/node_modules", "web/dist", "web/.vite", "demo_app_run*", "*.test", "*.exe")
$stage = Join-Path ([System.IO.Path]::GetTempPath()) ("patchpilot-demo-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $stage | Out-Null
Write-Host "Staging clean demo_app -> $stage"
robocopy $demo $stage /E /XD ($exclude | Where-Object { $_ -notmatch '\*' }) /XF "demo_app_run*" "*.test" "*.exe" | Out-Null
if ($LASTEXITCODE -ge 8) { throw "robocopy failed ($LASTEXITCODE)" }
$global:LASTEXITCODE = 0  # robocopy uses 0-7 for success

Push-Location $stage
try {
  git init -b $Branch | Out-Null
  git add . | Out-Null
  git -c user.name=$authorName -c user.email=$authorEmail commit -m $CommitMessage | Out-Null
  Write-Host "Committed clean demo app on branch '$Branch'."

  if ($StageOnly) {
    Write-Host "StageOnly: local repo ready at $stage (not pushed)."
    return
  }
  if (-not $RemoteUrl) {
    Write-Host ""
    Write-Host "No -RemoteUrl given. To publish:" -ForegroundColor Yellow
    Write-Host "  1. Create an empty PUBLIC repo on GitHub (no README/license)."
    Write-Host "  2. Re-run with: -RemoteUrl https://github.com/<you>/patchpilot-demo-app.git"
    Write-Host "     (local staged repo left at $stage)"
    return
  }
  git remote add origin $RemoteUrl
  Write-Host "Pushing to $RemoteUrl ..."
  git push -u origin $Branch
  Write-Host ""
  Write-Host "Published. Wire it into PatchPilot:" -ForegroundColor Green
  Write-Host "  Settings -> Git source -> Repository URL = $RemoteUrl"
  Write-Host "  Working branch = $Branch ; enable 'Create a branch per fix'."
  Write-Host "  Add a PAT + enable 'Push to remote' to push/merge to the remote, then Connect / Clone."
} finally {
  Pop-Location
}
