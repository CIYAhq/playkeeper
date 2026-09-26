# playkeeper.io, served by nginx on port 80: the pages in site/ built by
# cmd/site, with the docs from the repository's own Markdown, and the live
# demo. Build it from the repository root, since the site reads the product's
# code and docs and the demo is the dashboard in web/: docker build -f
# site/Dockerfile . (Coolify does the same, see README.md;
# Dockerfile.dockerignore keeps the context small). The tags are for humans;
# the digests decide.

# The live demo: the dashboard built with its sample data (web/src/demo).
FROM node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS demo
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build:demo

# The site: cmd/site builds every page, the assets and nginx's include.
FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS site
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download github.com/yuin/goldmark
COPY cmd/site/ cmd/site/
COPY internal/ internal/
COPY site/ site/
COPY web/src/assets/ web/src/assets/
COPY README.md CHANGELOG.md CONTRIBUTING.md SECURITY.md ./
COPY docs/ docs/
RUN go run ./cmd/site -root . -out /out

FROM nginx:1.30.5-alpine-slim@sha256:32463212baf0e7d91aded2e9b843a4f2b9e017804b8c9d5bae7b51dcef64389c

RUN rm -rf /usr/share/nginx/html/*
COPY site/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=site /out/nginx/site.conf /etc/nginx/playkeeper-site.conf
COPY --from=site /out/html/ /usr/share/nginx/html/
COPY --from=demo /web/dist-demo/ /usr/share/nginx/html/demo/

EXPOSE 80
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1
