FROM --platform=$BUILDPLATFORM golang:1.26.3 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /workspace
COPY go.mod go.sum ./
COPY runtime/go.mod runtime/go.sum ./runtime/
RUN go mod download && cd runtime && go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -C runtime -a -trimpath \
    -ldflags "-s -w -X github.com/kubeswarm/kubeswarm/cmd.version=${VERSION}" \
    -o /kubeswarm-controller ./cmd/kubeswarm-controller/

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /kubeswarm-controller .
USER 65532:65532
ENTRYPOINT ["/kubeswarm-controller"]
