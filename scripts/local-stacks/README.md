# Local stacks

## Headless demo

From the repository root, with Python 3, Go 1.26 or newer, and Docker Compose:

```bash
scripts/local-stacks/headless-demo/launch.py
```

Each run creates its own isolated Compose project (Postgres), builds `nori-local` and `nori-user`, starts the unified API + Postgres-native worker pool with a loopback model stub, and tears down its volumes on Ctrl-C.
