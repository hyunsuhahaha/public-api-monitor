# GCP 운영 서버

e2-micro 한 대(무료 한도: us-west1, 표준 디스크 30GB)에서 5분마다 `pamon -cycle -export`를 돌리고, DB는 디스크에 둔 채 Litestream으로 Cloud Storage에 실시간 복제합니다. 공개 주소는 Cloudflare Worker(`deploy/access`)이고, 이 서버는 Worker가 붙이는 비밀 헤더가 있는 요청만 받습니다.

## 처음부터 다시 만들기

```sh
P=pamon-2bad08                                    # 프로젝트 ID
SA=pamon-host@$P.iam.gserviceaccount.com
gcloud services enable compute.googleapis.com storage.googleapis.com secretmanager.googleapis.com --project $P
gcloud iam service-accounts create pamon-host --project $P
gcloud storage buckets create gs://$P-db --location=us-west1 --uniform-bucket-level-access --public-access-prevention --project $P
gcloud storage buckets add-iam-policy-binding gs://$P-db --member=serviceAccount:$SA --role=roles/storage.objectAdmin
gcloud compute firewall-rules create allow-web --network=default --allow=tcp:80,tcp:443 --target-tags=web --project $P
gcloud compute instances create pamon-host --project $P --zone us-west1-b --machine-type e2-micro \
  --image-family debian-12 --image-project debian-cloud --boot-disk-size 30GB --boot-disk-type pd-standard \
  --service-account $SA --scopes cloud-platform --tags web --metadata enable-oslogin=TRUE
gcloud compute addresses create pamon-ip --addresses=<VM 외부 IP> --region us-west1 --project $P   # IP 고정
```

Secret Manager(설정의 유일한 원본, VM 서비스 계정에는 앞의 둘만 읽기 권한):

| 이름 | 내용 |
|---|---|
| `pamon-env` | API 키, `PAMON_WEBHOOK_URL`, `PAMON_PUBLIC_URL`, `PAMON_SITE_URL`, 공개값(`PAMON_CF_BEACON` 등). docker `--env-file` 형식 |
| `caddy-env` | `PAMON_HOST=<IP를 -로 이은>.sslip.io`, `PAMON_GATEWAY_SECRET=<Worker의 GATEWAY_SECRET과 같은 값>` |
| `pamon-seed-key` | 옛 `db-seed` 릴리스 백업의 gpg 암호 (서버는 읽지 않음) |

```sh
gcloud secrets add-iam-policy-binding pamon-env --member=serviceAccount:$SA --role=roles/secretmanager.secretAccessor --project $P
gcloud secrets add-iam-policy-binding caddy-env --member=serviceAccount:$SA --role=roles/secretmanager.secretAccessor --project $P
```

서버에 [host](host)를 `/opt/pamon`으로 복사하고 `sudo bash /opt/pamon/setup.sh`. 디스크가 비어 있으면 `restore` 컨테이너가 Cloud Storage 복제본에서 DB를 되살린 뒤 시작합니다. Worker는 `wrangler.jsonc`의 `ORIGIN_URL`을 이 주소로 두고 배포합니다.

## 확인

```sh
systemctl list-timers pamon-cycle.timer
journalctl -u pamon-cycle.service -o cat | grep -E '"cycle done"|"export done"' | tail -2
docker logs pamon-litestream-1 --tail 3                       # wal segment written
docker run --rm --env-file /opt/pamon/host.env -v /tmp:/out -v /opt/pamon/litestream.yml:/etc/litestream.yml:ro \
  litestream/litestream:0.3.13 restore -config /etc/litestream.yml -o /out/restored.db /data/pamon.db   # 복원 연습
```
