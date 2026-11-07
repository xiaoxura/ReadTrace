# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/wxread-panel ./cmd/wxread-panel

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app \
    && mkdir -p /data \
    && chown app:app /data
COPY --from=build /out/wxread-panel /usr/local/bin/wxread-panel
USER app
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8720
ENTRYPOINT ["wxread-panel"]
CMD ["-addr", "0.0.0.0:8720", "-data", "/data", "-no-open"]
