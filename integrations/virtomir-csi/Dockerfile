# docker build -t ghcr.io/onurbaydas/virtomir-csi:dev integrations/virtomir-csi
# The Go build runs on the builder's own platform and cross-compiles for the target one.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X github.com/onurbaydas/virtomir/integrations/virtomir-csi/driver.Version=${VERSION}" -o /virtomir-csi .

# The node plugin formats and mounts: it needs blkid, mkfs and mount.
FROM debian:13-slim
LABEL org.opencontainers.image.source="https://github.com/onurbaydas/VirtoMir" \
      org.opencontainers.image.description="VirtoMir CSI driver: Kubernetes volumes as VirtoMir disks" \
      org.opencontainers.image.licenses="MPL-2.0"
RUN apt-get update && apt-get install -y --no-install-recommends e2fsprogs xfsprogs util-linux ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /virtomir-csi /usr/local/bin/virtomir-csi
ENTRYPOINT ["/usr/local/bin/virtomir-csi"]
