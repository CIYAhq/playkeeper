# playkeeper.io: the install page and the sizing guide, served by nginx on
# port 80. Coolify builds this directory (see README.md). The tag is for
# humans; the digest decides.
FROM nginx:1.30.5-alpine-slim@sha256:32463212baf0e7d91aded2e9b843a4f2b9e017804b8c9d5bae7b51dcef64389c

RUN rm -rf /usr/share/nginx/html/*
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html style.css copy.js favicon.svg /usr/share/nginx/html/
COPY sizing.html sizing.css sizing-nojs.css sizing.js sizing-data.js pip-wave.svg playkeeper-mark.svg /usr/share/nginx/html/

EXPOSE 80
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1
