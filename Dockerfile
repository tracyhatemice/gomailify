FROM golang:alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN apk add --no-cache git tzdata ca-certificates upx
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/gomailify ./cmd/gomailify && \
    upx --best --lzma /bin/gomailify

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
WORKDIR /app
COPY --from=builder /bin/gomailify /gomailify
WORKDIR /app

ENTRYPOINT ["/gomailify"]
CMD ["--config", "/app/config.yaml"]
