# playkeeper-names: the service behind free yourname.playkeeper.io addresses,
# on port 8080. Coolify builds it from the repository root (see README.md):
#   docker build -f services/names/Dockerfile .
# The tags are for humans; the digests decide.
FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/playkeeper-names cmd/playkeeper-names
COPY internal/names internal/names
COPY internal/store internal/store
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/playkeeper-names ./cmd/playkeeper-names

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
# A fixed ID, so a host directory mounted at /data can be given to it.
RUN addgroup -S -g 10001 names \
  && adduser -S -D -H -u 10001 -G names -s /sbin/nologin names \
  && mkdir -m 700 /data && chown names:names /data
COPY LICENSE THIRD_PARTY_NOTICES /usr/share/licenses/playkeeper-names/
COPY --from=build /out/playkeeper-names /usr/local/bin/playkeeper-names
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=40s --retries=3 \
  CMD ["playkeeper-names", "healthcheck"]
ENTRYPOINT ["playkeeper-names"]
CMD ["serve"]
