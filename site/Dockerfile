FROM ghcr.io/nginx/nginx-unprivileged:1.30.8-alpine@sha256:637c35e46af7bc0dd3d3bcf7162e004e13ab3b370fde7cd416e2ac410b5a985a
USER root
RUN apk add --no-cache 'zlib>=1.4-r0'
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html about.html privacy.html terms.html styles.css favicon.ico /usr/share/nginx/html/
COPY img /usr/share/nginx/html/img
RUN chmod -R u=rwX,go=rX /usr/share/nginx/html
USER 101
