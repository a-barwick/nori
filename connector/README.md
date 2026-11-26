# Nori Connector

Nori Connector pairs a computer running Ollama with a Nori account. The connector opens an outbound connection to Nori and sends model requests to Ollama on the same computer. Ollama and the connector must remain running for requests to complete.

## macOS

Download the signed Nori Connector disk image, move the app to Applications, and open it. Select **Get pairing code**, then enter that code in Nori under **More → Models → Home AI Server**. Allow the background item in System Settings if macOS asks. The helper runs after the user logs in.

The app needs macOS 13 or later. Install Ollama and at least one local model. Keep the Mac awake for scheduled work.

## Debian and Ubuntu

Install the package that matches the server's processor, then run:

~~~sh
sudo nori-connector-control pair
sudo nori-connector-control status
sudo nori-connector-control doctor
~~~

The package creates an unprivileged service account and starts the systemd service. Pairing restarts that service. The service and its credentials use `/var/lib/nori-connector`, accessible to the service account. Disconnect with `sudo nori-connector-control unpair`. To override the default relay or Ollama loopback port for a local setup, place `NORI_CONNECTOR_RELAY_URL` or `NORI_CONNECTOR_OLLAMA_URL` in the root-owned file `/etc/nori-connector/connector.env`, then restart the service.

## Builds

The release URL is set with `NORI_CONNECTOR_RELAY_URL` at build time. Use `NORI_CONNECTOR_VERSION` for the package version. The scripts build artifacts to the path supplied as their first argument.

~~~sh
NORI_CONNECTOR_RELAY_URL=https://relay.example \
  NORI_CONNECTOR_VERSION=1.0.0 \
  connector/macos/build.sh /tmp/nori-macos

NORI_CONNECTOR_RELAY_URL=https://relay.example \
  NORI_CONNECTOR_VERSION=1.0.0 \
  connector/linux/build.sh /tmp/nori-linux
~~~

The Linux build requires Go and nFPM v2.49.0. The macOS build requires Xcode command-line tools and Go. For a distributable macOS build, set `NORI_MAC_SIGNING_IDENTITY` to a Developer ID Application identity, set `NORI_CONNECTOR_DMG=1`, and provide a notarytool keychain profile as `NORI_MAC_NOTARY_PROFILE`. The script submits and staples the disk image. Without a signing identity, the app uses an ad hoc signature for local development.

The Go CLI accepts `--url`, `--state-dir`, and `--ollama-url` after the command. The relay URL must use HTTPS, with HTTP permitted only on loopback for local tests. The Ollama URL must use a numeric loopback address. `NORI_CONNECTOR_STATE_DIR`, `NORI_CONNECTOR_RELAY_URL`, and `NORI_CONNECTOR_OLLAMA_URL` provide corresponding environment defaults. Pairing uses the built-in relay URL when no override is provided.
