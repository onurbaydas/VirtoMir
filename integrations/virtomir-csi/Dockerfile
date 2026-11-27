# docker build -t ghcr.io/onurbaydas/virtomir-csi:dev integrations/virtomir-csi
# The Go build runs on the builder's own platform and cross-compiles for the target one.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X github.com/onurbaydas/virtomir/integrations/virtomir-csi/driver.Version=${VERSION}" -o /virtomir-csi .

# The node plugin formats and mounts: it needs blkid, mkfs and mount.
FROM debian:13-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
LABEL org.opencontainers.image.source="https://github.com/onurbaydas/VirtoMir" \
      org.opencontainers.image.description="VirtoMir CSI driver: Kubernetes volumes as VirtoMir disks" \
      org.opencontainers.image.licenses="MPL-2.0"
RUN apt-get update && apt-get install -y --no-install-recommends e2fsprogs xfsprogs util-linux ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /virtomir-csi /usr/local/bin/virtomir-csi
ENTRYPOINT ["/usr/local/bin/virtomir-csi"]
