FROM nginxinc/nginx-unprivileged:1.27-alpine@sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0
COPY . /usr/share/nginx/html/
USER root
RUN chmod -R u=rwX,go=rX /usr/share/nginx/html
RUN sed -i 's|location / {|location / {\n        add_header X-Robots-Tag "noindex" always;|' /etc/nginx/conf.d/default.conf
USER 101
