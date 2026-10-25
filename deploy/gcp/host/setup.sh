#!/usr/bin/env bash
# Runs on the VM as root, from a copy of deploy/gcp/host in /opt/pamon. Idempotent: run
# again after changing a file here or a secret. Secret Manager is the one source of the
# settings; the VM's service account may read them and nothing else writes them:
#   pamon-env   the monitor's keys and settings (docker --env-file format), with
#               PAMON_INGEST_TOKEN for the probes in other locations (openssl rand -hex 32)
#   caddy-env   PAMON_HOST=... PAMON_GATEWAY_SECRET=...
set -euo pipefail
cd /opt/pamon
umask 077
for s in pamon-env caddy-env; do
  gcloud secrets versions access latest --secret="$s" > "${s%-env}.env"
done
project=$(curl -fsS -H 'Metadata-Flavor: Google' http://metadata.google.internal/computeMetadata/v1/project/project-id)
echo "PAMON_BUCKET=$project-db" > host.env
umask 022

# 1 GB of RAM: a swap file keeps a slow cycle from being killed.
if ! swapon --show | grep -q /swapfile; then
  fallocate -l 1G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
  grep -q /swapfile /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

command -v docker > /dev/null || curl -fsSL https://get.docker.com | sh
command -v rsync > /dev/null || apt-get install -y rsync

# The image runs as the distroless nonroot user (65532).
mkdir -p /var/lib/pamon/data /var/lib/pamon/site /var/lib/pamon/site-next
chown -R 65532:65532 /var/lib/pamon

docker compose up -d --pull always --remove-orphans

cp pamon-cycle.service pamon-cycle.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now pamon-cycle.timer
systemctl list-timers pamon-cycle.timer --no-pager
