FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal/ ./internal/
# modernc.org/sqlite is pure Go: no cgo, static binary.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /timemore-dot-prometheus-exporter . \
 && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /timemore-dot-prometheus-exporter /timemore-dot-prometheus-exporter
# Owned by nonroot so a named volume mounted here is writable.
COPY --from=build --chown=65532:65532 /data /data
USER nonroot:nonroot
ENV DB_PATH=/data/caparao.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/timemore-dot-prometheus-exporter"]
