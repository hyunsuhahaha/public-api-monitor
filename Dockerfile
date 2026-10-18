# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# cross-compiled on the build machine (no emulation for the arm64 image)
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /pamon ./cmd/pamon \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /pamon /pamon
COPY targets.yaml /etc/pamon/targets.yaml
# /data owned by the nonroot user: a new named volume takes this ownership on first mount
# (a root-owned /data made SQLite fail with "unable to open database file").
COPY --from=build --chown=65532:65532 /out/data /data
ENV PAMON_CONFIG=/etc/pamon/targets.yaml PAMON_DB=/data/pamon.db PAMON_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/pamon"]
