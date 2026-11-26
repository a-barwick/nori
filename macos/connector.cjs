"use strict";

const { spawn } = require("node:child_process");
const { join } = require("node:path");

const maxOutput = 64 * 1024;

function startCommand(executable, args, { input, timeout = 20_000, onOutput, acceptOutputOnFailure } = {}) {
  const env = { ...process.env };
  for (const key of Object.keys(env)) {
    if (key.startsWith("NORI_CONNECTOR_") || key === "NODE_OPTIONS") delete env[key];
  }
  const child = spawn(executable, args, { env, stdio: ["pipe", "pipe", "ignore"] });
  const output = [];
  let size = 0;
  const result = new Promise((resolve, reject) => {
    const timer = setTimeout(() => child.kill("SIGKILL"), timeout);
    child.stdout.on("data", (chunk) => {
      size += chunk.length;
      if (size > maxOutput) {
        child.kill("SIGKILL");
        return;
      }
      output.push(chunk);
      onOutput?.(Buffer.concat(output).toString("utf8"));
    });
    child.stdin.on("error", () => {});
    child.stdin.end(input);
    child.once("error", reject);
    child.once("close", (code) => {
      clearTimeout(timer);
      if (code === 0 || (acceptOutputOnFailure && size > 0 && size <= maxOutput)) resolve(Buffer.concat(output).toString("utf8"));
      else reject(new Error("The Mac helper couldn’t complete the request."));
    });
  });
  result.catch(() => {});
  return { result, stop: () => {
    if (!child.kill("SIGTERM")) return;
    const timer = setTimeout(() => child.kill("SIGKILL"), 3_000);
    child.once("close", () => clearTimeout(timer));
  } };
}

function readResult(output) {
  try {
    return JSON.parse(output);
  } catch {
    throw new Error("The Mac helper returned an unexpected response.");
  }
}

function nativeCommands(binaryDir, { webOrigin }) {
  const connector = (command, options) => startCommand(join(binaryDir, "nori-connector"),
    [command, "--json"], options);
  const service = async (command) => {
    const output = await startCommand(join(binaryDir, "nori-desktop-service"), [command]).result;
    return output ? readResult(output) : undefined;
  };
  const keychain = async (action, value) => {
    const account = "com.nori.mac.google-session.v1:" + webOrigin;
    const input = JSON.stringify({ action, account, value });
    const output = await startCommand(join(binaryDir, "nori-connector-keychain"), [], { input }).result;
    const response = readResult(output);
    if (response.error) throw new Error("Nori couldn’t access the macOS Keychain.");
    return response.value;
  };
  return { connector, service, keychain };
}

class HomeConnector {
  #commands;
  #pairing;
  #action;

  constructor(commands) {
    this.#commands = commands;
  }

  async status() {
    const [service, output] = await Promise.all([
      this.#commands.service("status"),
      this.#commands.connector("status").result,
    ]);
    const status = readResult(output);
    return {
      background: service.background,
      paired: status.paired === true,
      state: status.state,
      ollama_available: status.ollama_available === true,
      revocationPending: !this.#pairing && status.revocation_pending === true,
    };
  }

  async models() {
    const output = await this.#commands.connector("doctor", { acceptOutputOnFailure: true }).result;
    const status = readResult(output);
    return { ollama_available: status.ollama_available === true, models: status.models ?? [] };
  }

  async register() {
    return this.#exclusive(async () => {
      if ((await this.status()).revocationPending) throw new Error("Finish disconnecting this Mac before enabling Home AI.");
      const service = await this.#commands.service("register");
      return { background: service.background, paired: false };
    });
  }

  async openSettings() {
    await this.#commands.service("settings");
  }

  async beginPairing() {
    return this.#exclusive(async () => {
      if (this.#pairing) throw new Error("A pairing is already in progress.");
      const status = await this.status();
      if (status.background !== "enabled" || status.paired || status.revocationPending) {
        throw new Error("Approve background access and finish any previous disconnect before pairing.");
      }

      await this.#commands.connector("desktop-enable").result;
      let ready;
      let failed;
      const grant = new Promise((resolve, reject) => { ready = resolve; failed = reject; });
      grant.catch(() => {});
      const command = this.#commands.connector("pair", {
        timeout: 11 * 60_000,
        onOutput(output) {
          const newline = output.indexOf("\n");
          if (newline === -1) return;
          try {
            const first = readResult(output.slice(0, newline));
            if (first.type === "pairing" && typeof first.user_code === "string" && /^[A-Z0-9-]{6,32}$/.test(first.user_code)) {
              ready({ user_code: first.user_code });
            } else {
              failed(new Error("The relay did not return a pairing code."));
            }
          } catch (error) {
            failed(error);
          }
        },
      });
      const completion = command.result.then((output) => {
        const last = output.trim().split("\n").at(-1);
        if (readResult(last).type !== "paired") throw new Error("The Mac did not finish pairing.");
      });
      completion.catch(failed);
      this.#pairing = { command, completion };
      const timeout = setTimeout(() => failed(new Error("Nori couldn’t start pairing. Try again.")), 25_000);
      try {
        return await grant;
      } catch (error) {
        await this.#stopPairing();
        throw error;
      } finally {
        clearTimeout(timeout);
      }
    });
  }

  async finishPairing() {
    const pairing = this.#pairing;
    if (!pairing) throw new Error("Start pairing this Mac again.");
    try {
      await pairing.completion;
      if (this.#pairing !== pairing) throw new Error("Pairing was cancelled.");
    } finally {
      if (this.#pairing === pairing) this.#pairing = undefined;
    }
  }

  get pairingInProgress() {
    return Boolean(this.#pairing);
  }

  async #stopPairing() {
    const pairing = this.#pairing;
    if (!pairing) return;
    pairing.command.stop();
    await pairing.completion.catch(() => {});
    if (this.#pairing === pairing) this.#pairing = undefined;
  }

  async disconnect() {
    return this.#exclusive(async () => {
      await this.#stopPairing();
      return this.#disable();
    }, true);
  }

  async retryRevocation() {
    return this.#exclusive(async () => {
      if (this.#pairing) return { revocationPending: false };
      const status = await this.status();
      if (status.revocationPending && !status.paired) return this.#disable();
      const result = readResult(await this.#commands.connector("desktop-revoke-pending").result);
      return { revocationPending: result.revocation_pending === true };
    }, true);
  }

  async #disable() {
    const result = await this.#commands.connector("desktop-disable").result
      .then(readResult)
      .finally(() => this.#commands.service("unregister"));
    return { revocationPending: result.revocation_pending === true };
  }

  async #exclusive(action, wait = false) {
    if (this.#action) {
      if (!wait) throw new Error("Finish the current Home AI setup action first.");
      await this.#action.catch(() => {});
      return this.#exclusive(action, wait);
    }
    const pending = action();
    this.#action = pending;
    try {
      return await pending;
    } finally {
      this.#action = undefined;
    }
  }
}

module.exports = { HomeConnector, nativeCommands };
