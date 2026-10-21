# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM scratch
LABEL org.opencontainers.image.title="real-time-ui-update-microservice" \
      org.opencontainers.image.description="Plug-and-play Go microservice that fans backend events out to browsers over WebSockets" \
      org.opencontainers.image.source="https://github.com/SantiagoLopezDeharo/real-time-ui-update-microservice" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/server /server
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/server"]
