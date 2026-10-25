# docker build -t ghcr.io/onurbaydas/virtomir-operator:dev integrations/virtomir-operator
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /virtomir-operator .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /virtomir-operator /virtomir-operator
USER 65532:65532
ENTRYPOINT ["/virtomir-operator"]
