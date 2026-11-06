FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY . /usr/share/nginx/html/
USER root
# Files are readable and folders such as img/ can be opened. A 644 folder makes nginx return 403 for its files.
RUN chmod -R u=rwX,go=rX /usr/share/nginx/html
# noindex also covers the Markdown copies and llms.txt, which can't carry a meta tag.
RUN sed -i 's|location / {|location / {\n        add_header X-Robots-Tag "noindex" always;|' /etc/nginx/conf.d/default.conf
USER 101
