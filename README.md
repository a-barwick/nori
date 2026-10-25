# Nori

Nori is a personal assistant you text. This repository holds the whole product:

- [core](core/README.md): the Go agent service and its infrastructure.
- [web](web/README.md): the Next.js desktop web interface.

## Run

```sh
make dev     # web on http://127.0.0.1:3210, core on :8080
make check   # Go vet and gofmt, then TypeScript and ESLint
```

Install web's dependencies first with `cd web && bun install`. Keep configuration
in a single gitignored, mode-600 `.env` at this repository's root; see
[.env.example](.env.example) for names. `make dev` loads it for both apps without
copying credentials into either app. Google Cloud access uses Application Default
Credentials. The deployed POC requires the existing private GCP infrastructure;
see [core setup](core/README.md) and [POC operations](core/infrastructure/poc/README.md).
