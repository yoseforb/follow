FROM alpine:3.21

# Pin MC_RELEASE to a known-good MinIO Client build. Bump explicitly
# when a newer version is validated — never implicitly via apk.
ARG MC_RELEASE=RELEASE.2025-08-13T08-35-41Z

RUN apk add --no-cache postgresql17-client tzdata ca-certificates curl age \
 && curl -fsSL "https://dl.min.io/client/mc/release/linux-amd64/archive/mc.${MC_RELEASE}" \
      -o /usr/local/bin/mc \
 && chmod +x /usr/local/bin/mc \
 && apk del --no-cache curl

COPY scripts/backup.sh /usr/local/bin/backup.sh
RUN chmod +x /usr/local/bin/backup.sh

ENTRYPOINT ["/bin/sh", "-c", "/usr/local/bin/backup.sh install-cron && exec crond -f -d 8 -L /dev/stdout"]
