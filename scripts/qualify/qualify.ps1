# qualify.ps1: Windows counterpart of qualify.sh (M27.02). It compiles the realengine test
# binary and netprobe first (network available), copies them with hclaro, the engines and the
# mirror into a run directory, then starts the test binary with a cleared environment holding
# only the harness inputs, a minimal PATH and SystemRoot. No Go toolchain runs inside.
# There is no enforced isolation on Windows yet (F-0 Windows NOT_TESTED, OD-4: the per-program
# firewall is decided before M27.07), so the default is "proxy" and runs are weak-proxy.
#
#   scripts\qualify\qualify.ps1 -Terraform C:\abs\terraform.exe -Tofu C:\abs\tofu.exe -Mirror <dir> -Out <dir>
param(
  [string]$Terraform = "",
  [string]$Tofu = "",
  [Parameter(Mandatory = $true)][string]$Mirror,
  [Parameter(Mandatory = $true)][string]$Out,
  [string]$Hclaro = "",
  [ValidateSet("proxy", "none")][string]$Isolation = "proxy",
  [string]$RunFilter = "^TestReal",
  [switch]$Keep
)
$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
if (-not $Hclaro) { $Hclaro = Join-Path $repo "bin\hclaro.exe" }
if (-not (Test-Path $Hclaro)) { throw "hclaro binary missing: $Hclaro (run make binary)" }

$run = Join-Path ([IO.Path]::GetTempPath()) ("hclaro-qualify." + [Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Path $run, "$run\engines", "$run\mirror", "$run\out" | Out-Null
$status = 1
try {
  $commit = (git -C $repo rev-parse HEAD).Trim()
  $dirty = if (git -C $repo status --porcelain) { "true" } else { "false" }
  $pkg = "hclaro/internal/qualify"
  $ldflags = "-X $pkg.buildCommit=$commit -X $pkg.buildDirty=$dirty -X $pkg.buildHome=$HOME -X $pkg.buildRepo=$repo -X $pkg.buildUser=$env:USERNAME"
  Push-Location $repo
  try {
    go test -c -tags realengine -ldflags $ldflags -o "$run\qualify.test.exe" ./internal/qualify
    if ($LASTEXITCODE -ne 0) { throw "go test -c failed" }
    $env:CGO_ENABLED = "0"
    go build -o "$run\netprobe.exe" ./internal/qualify/netprobe
    if ($LASTEXITCODE -ne 0) { throw "netprobe build failed" }
  } finally { Pop-Location }
  Copy-Item $Hclaro "$run\hclaro.exe"
  $tf = ""; $tofuPath = ""
  if ($Terraform) { Copy-Item $Terraform "$run\engines\terraform.exe"; $tf = "$run\engines\terraform.exe" }
  if ($Tofu) { Copy-Item $Tofu "$run\engines\tofu.exe"; $tofuPath = "$run\engines\tofu.exe" }
  foreach ($f in "mirror-manifest.json", "terraform.tfrc", "tofu.tfrc") { Copy-Item (Join-Path $Mirror $f) "$run\mirror\" }
  foreach ($d in "terraform", "tofu") { Copy-Item -Recurse (Join-Path $Mirror $d) "$run\mirror\$d" }
  & "$run\netprobe.exe" control | Out-File -Encoding ascii "$run\f0-control.json"

  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = "$run\qualify.test.exe"
  $psi.Arguments = "-test.v -test.timeout 90m -test.run `"$RunFilter`""
  $psi.WorkingDirectory = $run
  $psi.UseShellExecute = $false
  $psi.Environment.Clear()
  $vars = @{
    "PATH" = "$env:SystemRoot\System32"; "SystemRoot" = $env:SystemRoot
    "HCLARO_QUAL_TERRAFORM" = $tf; "HCLARO_QUAL_TOFU" = $tofuPath; "HCLARO_BIN" = "$run\hclaro.exe"
    "HCLARO_QUAL_MIRROR" = "$run\mirror"; "HCLARO_QUAL_OUT" = "$run\out"; "HCLARO_QUAL_ISOLATION" = $Isolation
  }
  if ($Keep) { $vars["HCLARO_QUAL_KEEP"] = "1" }
  foreach ($k in $vars.Keys) { $psi.Environment[$k] = $vars[$k] }
  $p = [System.Diagnostics.Process]::Start($psi)
  $p.WaitForExit()
  $code = $p.ExitCode
  New-Item -ItemType Directory -Force -Path $Out | Out-Null
  Copy-Item -Recurse -Force "$run\out\*" $Out
  $status = $code
  if (-not (Test-Path (Join-Path $Out "run.json"))) { Write-Error "qualify: no run.json written"; $status = 1 }
  Write-Host "qualify: test binary exit $code; results in $Out"
} finally {
  if ($status -ne 0 -and $Keep) {
    Write-Host "raw run directory kept (unsanitised, never copy it into the repository): $run"
  } else {
    Remove-Item -Recurse -Force $run -ErrorAction SilentlyContinue
  }
}
exit $status
