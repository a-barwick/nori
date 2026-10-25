# Local Nori infrastructure

This Compose stack packages the separate nori-core and nori-web images with one
Temporal server, one Postgres instance, and one persistent shell/Chromium computer.
The Go service still returns its greeting; the web UI is still a local demo.
Neither uses the database, Temporal, or computer yet. Agent workers and the
internal delivery loop will share the Go process when implemented.

**This configuration is for one local developer machine.** Postgres uses trust
authentication without passwords for selected roles on an explicit internal
control subnet, with a gateway rejection rule for OrbStack cross-bridge traffic.
It has no host port and the computer is not on that bridge. Do not deploy this
configuration to a server or reuse its database authentication there.

## Start locally

Requires Docker with Compose. From `core/`:

```sh
# Builds independent core, web, and computer images from their own Dockerfiles.
docker compose -f infrastructure/compose.yaml -f infrastructure/compose.local.yaml build
docker compose -f infrastructure/compose.yaml up -d
```

The project name is `nori-dev`. Open the [web demo](http://127.0.0.1:3100)
on loopback port 3100. Core has no published host port; inspect its health
through the web container on the control bridge (command below). No keys or
`.env` file are needed or passed to the computer.
Core's existing credential file is not mounted into any service.

To run independently released images locally, skip the build override:

```sh
NORI_CORE_IMAGE=your-core-image:release-a \
NORI_WEB_IMAGE=your-web-image:release-b \
NORI_COMPUTER_IMAGE=your-computer-image:tools-v1 \
docker compose -f infrastructure/compose.yaml up -d
```

`NORI_WEB_PORT` optionally overrides 3100. Each application keeps its own image and release. The web image does
not contain the Go service and the Go image does not contain the web app.

## Inspect and stop

```sh
docker compose -f infrastructure/compose.yaml ps -a
docker compose -f infrastructure/compose.yaml exec nori-web node -e 'fetch("http://nori-core:8080/healthz").then(r => { if (!r.ok) throw Error(r.status); return r.text(); }).then(console.log)'
curl --fail http://127.0.0.1:3100/healthz
docker compose -f infrastructure/compose.yaml exec postgres psql -U nori -d nori -c 'select current_database();'
docker compose -f infrastructure/compose.yaml run --rm --no-deps --entrypoint temporal temporal-namespace operator cluster health --address temporal:7233
docker compose -f infrastructure/compose.yaml exec computer curl --fail --silent http://localhost:9222/json/version
docker compose -f infrastructure/compose.yaml exec computer bash
docker compose -f infrastructure/compose.yaml logs --tail 100
docker compose -f infrastructure/compose.yaml stop
```

Use `up -d` to resume. `down` removes containers and networks while retaining
named volumes; **do not use `down -v` unless intentionally deleting all local
Postgres, workspace, and browser data**. Volumes are
`nori-dev_postgres-data`, `nori-dev_computer-workspace`, and
`nori-dev_computer-home`. Restart policies recover long-running services;
schema and namespace setup are one-shot jobs and failures stop dependent startup.
Container JSON logs rotate at three 10 MB files per container.

## Persistence and boundaries

Postgres's `pg_hba.conf` trusts only the app/Temporal database-role pairs from
`172.28.40.0/24`, rejects that subnet's gateway `172.28.40.1` (OrbStack NATs
cross-bridge traffic through it), and rejects all other TCP sources. The subnet
and gateway must match Compose. Change both files if the subnet conflicts with
your host network. Unix socket administration stays local to the Postgres
container. This is a local no-secret setup, not a server authentication design.

Postgres owns three databases: `nori`, `temporal`, and `temporal_visibility`, with
separate app and Temporal roles. Initial database creation runs only for an empty
Postgres volume. Matching Temporal server/admin-tools 1.31.0 images use the vendor
SQL schema tools on each Compose startup; setup and schema updates preserve
existing data. The namespace job checks service health and creates `nori` only
when absent. Its retention is three days. The four history shards are fixed at
first initialization; changing that count requires a new Temporal cluster.
Postgres visibility replaces Elasticsearch. There is no Temporal UI.

The `control` network is internal: core, web, Postgres, and Temporal use it.
Web also joins an `ingress` bridge for Docker loopback port publishing and
outbound access. The computer is alone on the separate `computer` bridge and
has outbound internet access, a stable `computer` hostname, and CDP on port 9222 inside that bridge.
It receives no model credentials, Docker socket, or host directory mounts.
Named volumes preserve `/workspace` and `/home/agent`, including the Chromium
profile at `/home/agent/chromium`. Normal container shutdown asks Chromium to
close over local CDP so pending profile writes can flush, with a bounded fallback
to process termination. Browser-state survival after crashes or power loss is
unproven. Put each task in `/workspace/tasks/<task-id>`.
Bake system-tool changes into a new computer image version; installing software
in the container's writable layer will not survive recreation.

Chromium runs headless as the unprivileged `agent` user with its browser sandbox
disabled for container compatibility. The container drops all Linux capabilities
and disallows privilege escalation. This is a trusted personal computer for local
work, not isolation for hostile code. Browser commands can access its persistent
files and outbound network. The computer is not a member of the control
bridge, but **OrbStack permits cross-bridge traffic by direct IP**. Live checks
confirmed that core's greeting API and Temporal are reachable this way; the
Postgres rejection is enforced by `pg_hba.conf`, not bridge isolation. The
computer can also reach host services through `host.docker.internal`, including
loopback-published ports. Core has no host publication; the web demo remains
reachable through the host. These networks do not isolate host services or the
control APIs. This scaffold has no model keys, real app records, or running
agent. Do not wire those capabilities to the computer until an access policy is
implemented.

Core has no configured computer adapter or public-internet egress on its internal
network. Connection and access policies are still needed when wiring the real
loop. CDP checks prove browser readiness, not task execution or agent behavior. No GCP
capacity or deployment has been validated.

## Source and existing cloud scaffold

This configuration follows Temporal's
[self-hosted deployment guidance](https://docs.temporal.io/self-hosted-guide/deployment)
and [Postgres Compose example](https://github.com/temporalio/samples-server/blob/main/compose/docker-compose-postgres.yml),
using matching server/admin-tools images and the vendor schema tool. The small
namespace script checks the vendor CLI's JSON output before creating the namespace.

OrbStack's [network documentation](https://docs.orbstack.dev/docker/network)
explains host/direct-IP access; its maintainer confirms cross-bridge access in
[issue 1944](https://github.com/orbstack/orbstack/issues/1944).

The existing `k8s.yaml` remains a historical GKE starter manifest. It is not the
selected deployment path for this local slice and does not describe this stack.
No cloud resources are created by these instructions.
