# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.1.0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/readtrace ./cmd/readtrace

# 二进制与 data 目录位于同一目录：/opt/readtrace/readtrace + /opt/readtrace/data
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app \
    && mkdir -p /opt/readtrace/data \
    && chown -R app:app /opt/readtrace
COPY --from=build /out/readtrace /opt/readtrace/readtrace
USER app
WORKDIR /opt/readtrace
VOLUME ["/opt/readtrace/data"]
EXPOSE 8720
ENTRYPOINT ["./readtrace", "-addr", "0.0.0.0:8720", "-no-open", "-data", "/opt/readtrace/data"]
