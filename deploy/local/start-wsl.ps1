# Starts pamon in WSL in the background (hidden window). The hidden wsl.exe keeps the WSL
# VM alive; stop-wsl.ps1 (or ending that wsl.exe) stops the monitor.
# Status page: http://localhost:8080
# A start is only done when /readyz answers: it waits up to 2 minutes and fails otherwise
# (docs/postmortems/2026-10-19-startup-deadlock.md: a deploy hung on start unnoticed).
$full = (Resolve-Path "$PSScriptRoot\run-wsl.sh").Path
$script = "/mnt/" + $full.Substring(0, 1).ToLower() + $full.Substring(2).Replace('\', '/')
$running = wsl -d Ubuntu -e bash -c 'test -f ~/.pamon/run.pid && kill -0 $(cat ~/.pamon/run.pid) 2>/dev/null && echo yes'
if ($running -eq "yes") { "pamon is already running"; exit 0 }
Start-Process wsl.exe -ArgumentList @("-d", "Ubuntu", "-e", "bash", "`"$script`"") -WindowStyle Hidden
$deadline = (Get-Date).AddMinutes(2)
while ((Get-Date) -lt $deadline) {
  try {
    $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 http://localhost:8080/readyz
    if ($r.StatusCode -eq 200) { "ready: http://localhost:8080 (logs: wsl -d Ubuntu -e tail -f ~/.pamon/pamon.log)"; exit 0 }
  } catch { }
  Start-Sleep -Seconds 3
}
Write-Error "pamon did not become ready in 2 minutes. Last log lines:"
wsl -d Ubuntu -e tail -n 20 ~/.pamon/pamon.log
exit 1
