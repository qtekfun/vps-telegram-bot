FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/vps-telegram-bot ./cmd/vps-telegram-bot

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
LABEL org.opencontainers.image.source="https://github.com/qtekfun/vps-telegram-bot" \
      org.opencontainers.image.description="Telegram bot to check and operate the Docker containers of one VPS" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/vps-telegram-bot /vps-telegram-bot
USER nonroot:nonroot
ENTRYPOINT ["/vps-telegram-bot"]
