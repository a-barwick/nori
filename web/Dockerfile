# syntax=docker/dockerfile:1
FROM oven/bun:1.4.3-debian@sha256:923bedb355f7746666659f71e724998159a5c8c54bd1cb1030e1975a0698415b AS build
WORKDIR /app/web
ENV NEXT_TELEMETRY_DISABLED=1
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web .
ARG NEXT_PUBLIC_SENTRY_DSN
ARG NEXT_PUBLIC_SENTRY_ENVIRONMENT
ARG NEXT_PUBLIC_SENTRY_RELEASE
ARG NEXT_PUBLIC_SENTRY_TRACES_SAMPLE_RATE
ARG SENTRY_ORG
ARG SENTRY_PROJECT
ARG SENTRY_RELEASE
RUN --mount=type=secret,id=sentry_auth_token,required=false \
    if [ -f /run/secrets/sentry_auth_token ]; then export SENTRY_AUTH_TOKEN="$(cat /run/secrets/sentry_auth_token)"; fi && \
    mkdir -p public && bun run build

FROM node:24.22.3-trixie-slim@sha256:4d00acb4bcc42c454c4eb801526f7ad7f772ea3bd4fd168c5cc2e384abcbb9d4 AS runtime
WORKDIR /app
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx
ARG SENTRY_RELEASE
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    SENTRY_RELEASE=${SENTRY_RELEASE} \
    HOSTNAME=0.0.0.0 \
    PORT=8080
COPY --from=build --chown=node:node /app/web/.next/standalone ./
COPY --from=build --chown=node:node /app/web/.next/static ./.next/static
COPY --from=build --chown=node:node /app/web/public ./public
COPY --from=build --chown=node:node /app/web/ws-server.mjs ./ws-server.mjs
USER node
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD node -e "fetch('http://127.0.0.1:'+process.env.PORT+'/api/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"
CMD ["node", "ws-server.mjs"]
