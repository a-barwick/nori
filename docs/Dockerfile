FROM ghcr.io/nginx/nginx-unprivileged:1.30.8-alpine@sha256:637c35e46af7bc0dd3d3bcf7162e004e13ab3b370fde7cd416e2ac410b5a985a
COPY . /usr/share/nginx/html/
USER root
RUN apk add --no-cache 'zlib>=1.4-r0' && chmod -R u=rwX,go=rX /usr/share/nginx/html
RUN sed -i 's|location / {|location / {\n        absolute_redirect off;\n        try_files $uri $uri.html $uri/ =404;\n        add_header X-Robots-Tag "noindex" always;|' /etc/nginx/conf.d/default.conf
USER 101
