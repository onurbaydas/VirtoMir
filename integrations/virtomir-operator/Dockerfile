# docker build -t ghcr.io/onurbaydas/virtomir-operator:dev integrations/virtomir-operator
# The Go build runs on the builder's own platform and cross-compiles for the target one.
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /virtomir-operator .

FROM gcr.io/distroless/static:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
LABEL org.opencontainers.image.source="https://github.com/onurbaydas/VirtoMir" \
      org.opencontainers.image.description="VirtoMir operator: VirtoMir machines as Kubernetes objects" \
      org.opencontainers.image.licenses="MPL-2.0"
COPY --from=build /virtomir-operator /virtomir-operator
USER 65532:65532
ENTRYPOINT ["/virtomir-operator"]
