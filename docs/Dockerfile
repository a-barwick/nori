FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY --chmod=644 . /usr/share/nginx/html/
USER root
# noindex also covers the Markdown copies and llms.txt, which can't carry a meta tag.
RUN sed -i 's|location / {|location / {\n        add_header X-Robots-Tag "noindex" always;|' /etc/nginx/conf.d/default.conf
USER 101
