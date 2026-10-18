# Starts pamon in WSL in the background (hidden window). The hidden wsl.exe keeps the WSL
# VM alive; stop-wsl.ps1 (or ending that wsl.exe) stops the monitor.
# Status page: http://localhost:8080
$full = (Resolve-Path "$PSScriptRoot\run-wsl.sh").Path
$script = "/mnt/" + $full.Substring(0, 1).ToLower() + $full.Substring(2).Replace('\', '/')
$running = wsl -d Ubuntu -e bash -c 'test -f ~/.pamon/run.pid && kill -0 $(cat ~/.pamon/run.pid) 2>/dev/null && echo yes'
if ($running -eq "yes") { "pamon is already running"; exit 0 }
Start-Process wsl.exe -ArgumentList @("-d", "Ubuntu", "-e", "bash", "`"$script`"") -WindowStyle Hidden
"started: http://localhost:8080 (logs: wsl -d Ubuntu -e tail -f ~/.pamon/pamon.log)"
