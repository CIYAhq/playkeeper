# playkeeper-stats: the anonymous install and usage counts behind
# stats.playkeeper.io, on port 8080. Coolify builds it from the repository
# root (see README.md):
#   docker build -f services/stats/Dockerfile .
# The tags are for humans; the digests decide.
FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/playkeeper-stats cmd/playkeeper-stats
COPY internal/usage internal/usage
COPY internal/store internal/store
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/playkeeper-stats ./cmd/playkeeper-stats

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
# A fixed ID, so a host directory mounted at /data can be given to it.
RUN addgroup -S -g 10001 stats \
  && adduser -S -D -H -u 10001 -G stats -s /sbin/nologin stats \
  && mkdir -m 700 /data && chown stats:stats /data
COPY LICENSE THIRD_PARTY_NOTICES /usr/share/licenses/playkeeper-stats/
COPY --from=build /out/playkeeper-stats /usr/local/bin/playkeeper-stats
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
  CMD ["playkeeper-stats", "healthcheck"]
ENTRYPOINT ["playkeeper-stats"]
CMD ["serve"]
