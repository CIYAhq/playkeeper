# playkeeper.io: the install page, the sizing guide and the live demo, served
# by nginx on port 80. Build it from the repository root, since the demo is
# the dashboard in web/: docker build -f site/Dockerfile . (Coolify does the
# same, see README.md; Dockerfile.dockerignore keeps the context small). The
# tags are for humans; the digests decide.

# The live demo: the dashboard built with its sample data (web/src/demo).
FROM node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS demo
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build:demo

FROM nginx:1.30.5-alpine-slim@sha256:32463212baf0e7d91aded2e9b843a4f2b9e017804b8c9d5bae7b51dcef64389c

RUN rm -rf /usr/share/nginx/html/*
COPY site/nginx.conf /etc/nginx/conf.d/default.conf
COPY site/index.html site/style.css site/copy.js site/favicon.svg /usr/share/nginx/html/
COPY site/sizing.html site/sizing.css site/sizing-nojs.css site/sizing.js site/sizing-data.js site/pip-wave.svg site/playkeeper-mark.svg /usr/share/nginx/html/
COPY --from=demo /web/dist-demo/ /usr/share/nginx/html/demo/

EXPOSE 80
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1
