# Stops the background pamon started by start-wsl.ps1.
wsl -d Ubuntu -e bash -c 'test -f ~/.pamon/run.pid && kill $(cat ~/.pamon/run.pid) && echo stopped || echo "not running"'
