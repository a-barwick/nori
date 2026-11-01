FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY . /usr/share/nginx/html/
USER root
RUN rm /usr/share/nginx/html/AGENTS.md
USER 101
