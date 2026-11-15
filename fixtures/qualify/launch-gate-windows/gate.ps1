# M27.07 WIN1 gate (Windows variant of gate.sh; A6, A7, A7b only). Deterministic; no network;
# touches only ./.qual/ in the project directory it is started in. Exits 0 on release, 130 on
# an interrupt in normal mode, 3 on its hard timeout, 2 on a bad mode.
# PowerShell cannot trap CTRL_BREAK/CTRL_C itself, so a console control handler (compiled
# locally by Add-Type; the temporary build files go to TEMP) records the event like gate.sh's
# INT trap: "int" in .qual/interrupted, then exit 130 (normal) or keep polling (trap).
$ErrorActionPreference = "Stop"
$dir = Join-Path (Get-Location).Path ".qual"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
switch ($env:GATE_MODE) {
  "normal" { $exitOnInt = $true; $limit = 1200 }
  "trap" { $exitOnInt = $false; $limit = 9000 }
  default { exit 2 }
}
Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Runtime.InteropServices;
public static class QualGate {
  public delegate bool Handler(uint ctrlType);
  static Handler keep;
  [DllImport("kernel32.dll")] static extern bool SetConsoleCtrlHandler(Handler h, bool add);
  public static void Install(string marker, bool exitOnInt) {
    keep = t => {
      if (t != 0 && t != 1) { return false; } // CTRL_C_EVENT, CTRL_BREAK_EVENT only
      File.AppendAllText(marker, "int\n");
      if (exitOnInt) { Environment.Exit(130); }
      return true;
    };
    if (!SetConsoleCtrlHandler(keep, true)) { throw new InvalidOperationException("SetConsoleCtrlHandler failed"); }
  }
}
'@
[QualGate]::Install((Join-Path $dir "interrupted"), $exitOnInt)
[IO.File]::WriteAllText((Join-Path $dir "reached"), "")
$i = 0
while (-not (Test-Path (Join-Path $dir "release"))) {
  $i++
  if ($i -gt $limit) { exit 3 }
  Start-Sleep -Milliseconds 100
}
exit 0
