# Build natively on the runner and cross-compile for each target platform (no QEMU needed).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" -o /llamero .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /llamero /llamero
COPY LICENSE THIRD_PARTY_LICENSES /licenses/
EXPOSE 8092
VOLUME /data
VOLUME /config
ENTRYPOINT ["/llamero"]
