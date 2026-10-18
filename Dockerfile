# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pamon ./cmd/pamon

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /pamon /pamon
COPY targets.yaml /etc/pamon/targets.yaml
ENV PAMON_CONFIG=/etc/pamon/targets.yaml PAMON_DB=/data/pamon.db PAMON_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/pamon"]
