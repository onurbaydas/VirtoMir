# docker build -t ghcr.io/onurbaydas/virtomir-csi:dev integrations/virtomir-csi
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/onurbaydas/virtomir/integrations/virtomir-csi/driver.Version=${VERSION}" -o /virtomir-csi .

# The node plugin formats and mounts: it needs blkid, mkfs and mount.
FROM debian:13-slim
RUN apt-get update && apt-get install -y --no-install-recommends e2fsprogs xfsprogs util-linux ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /virtomir-csi /usr/local/bin/virtomir-csi
ENTRYPOINT ["/usr/local/bin/virtomir-csi"]
