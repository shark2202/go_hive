# syntax=docker/dockerfile:1
# Build a static hive-cloud binary and run it as an unprivileged user on a
# read-only root filesystem. No secret values are baked into the image.
ARG GO_VERSION=1.26.11

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/hive-cloud ./cmd/hive-cloud
RUN /out/hive-cloud version

FROM alpine:3.22
ARG APP_UID=65532
ARG APP_GID=65532
RUN addgroup -g "${APP_GID}" -S hive-cloud \
 && adduser -u "${APP_UID}" -S -G hive-cloud -H -s /sbin/nologin hive-cloud \
 && mkdir -p /var/lib/hive-cloud \
 && chown "${APP_UID}:${APP_GID}" /var/lib/hive-cloud
COPY --from=build --chown=${APP_UID}:${APP_GID} /out/hive-cloud /usr/local/bin/hive-cloud

USER ${APP_UID}:${APP_GID}
WORKDIR /var/lib/hive-cloud
ENV HIVE_CLOUD_LISTEN=0.0.0.0:8788 \
    HIVE_CLOUD_DATA_DIR=/var/lib/hive-cloud
EXPOSE 8788

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8788/health || exit 1

ENTRYPOINT ["/usr/local/bin/hive-cloud"]
CMD ["serve"]
