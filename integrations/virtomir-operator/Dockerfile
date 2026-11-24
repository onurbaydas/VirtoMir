# docker build -t ghcr.io/onurbaydas/virtomir-operator:dev integrations/virtomir-operator
# The Go build runs on the builder's own platform and cross-compiles for the target one.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /virtomir-operator .

FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.source="https://github.com/onurbaydas/VirtoMir" \
      org.opencontainers.image.description="VirtoMir operator: VirtoMir machines as Kubernetes objects" \
      org.opencontainers.image.licenses="MPL-2.0"
COPY --from=build /virtomir-operator /virtomir-operator
USER 65532:65532
ENTRYPOINT ["/virtomir-operator"]
