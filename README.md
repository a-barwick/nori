# Nori

Nori is a personal assistant you text. This repository holds the whole product:

- [core](core/README.md): the Go agent service and its infrastructure.
- [web](web/README.md): the Next.js desktop web interface.

Design:

- [Client contract](docs/client-contract.md): the protocol every client uses to talk to the agent service.
- [Separation of concerns](docs/separation-of-concerns.md): what the service owns, what clients own.

## Run

```sh
make dev     # web on http://127.0.0.1:3210, core on :8080
make check   # Go vet and gofmt, then TypeScript and ESLint
```

Install web's dependencies first with `cd web && bun install`. Each app reads its keys from its own `.env`; see the `.env.example` next to it.
