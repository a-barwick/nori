# Nori Connector

Nori Connector pairs a computer running Ollama with a Nori account. The connector opens an outbound connection to Nori and sends model requests to Ollama on the same computer. Ollama and the connector must remain running for requests to complete.

## macOS

macOS Home AI sharing is built into **Nori for Mac** (`macos/`). Download and install the signed Nori for Mac disk image, open **More → Models → Use this Mac as a Home AI Server**, select an installed Ollama model, and allow the background item in System Settings if macOS asks. See [`macos/README.md`](../macos/README.md) for setup, signing, and build instructions.

## Debian and Ubuntu

Install the `.deb` package that matches the server's processor (`amd64` or `arm64`), then pair and verify the host:

~~~sh
sudo nori-connector-control pair
sudo nori-connector-control status
sudo nori-connector-control doctor
~~~

The package creates an unprivileged `nori-connector` service account, installs `/usr/lib/systemd/system/nori-connector.service`, and enables the unit without starting it before pairing. Running `sudo nori-connector-control pair` pairs the host and starts or restarts the systemd service. State and credentials live under `/var/lib/nori-connector` (`0700`, owned by `nori-connector`). Disconnect and stop the service with `sudo nori-connector-control unpair`.

To override the default relay or Ollama loopback URL for a local setup, create a root-owned regular file `/etc/nori-connector/connector.env` with mode `0600` containing `NORI_CONNECTOR_RELAY_URL` or `NORI_CONNECTOR_OLLAMA_URL`, then restart `nori-connector.service`.

## Builds

The relay origin is embedded via `NORI_CONNECTOR_RELAY_URL` (origin-only HTTPS URL, or loopback HTTP for local testing) and the package version via `NORI_CONNECTOR_VERSION`. The Linux build script writes artifacts to the path supplied as its first argument:

~~~sh
NORI_CONNECTOR_RELAY_URL=https://relay.example \
  NORI_CONNECTOR_VERSION=1.0.0 \
  connector/linux/build.sh /tmp/nori-linux
~~~

The Linux build requires Go and `nfpm` v2.49.0. For macOS builds, use `macos/scripts/build.sh` (documented in [`macos/README.md`](../macos/README.md)).

The underlying Go CLI (`core/cmd/nori-connector`) accepts `--url`, `--state-dir`, and `--ollama-url` after the subcommand (`nori-connector-control` pins `--state-dir` to `/var/lib/nori-connector`). The relay URL must be an origin-only HTTPS URL (with loopback HTTP permitted for local tests), and the Ollama URL must use a numeric loopback address. `NORI_CONNECTOR_STATE_DIR`, `NORI_CONNECTOR_RELAY_URL`, and `NORI_CONNECTOR_OLLAMA_URL` provide corresponding environment defaults.
