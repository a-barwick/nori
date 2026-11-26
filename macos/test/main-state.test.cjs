"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { EventEmitter } = require("node:events");
const { createAppController } = require("../main.cjs");
const { SignInError } = require("../google-auth.cjs");

function createFakeElectron() {
  const windows = [];
  const sessions = new Map();
  const ipcHandlers = new Map();
  const appEvents = new EventEmitter();
  const clearedStorage = [];
  const externalURLs = [];
  const errorBoxes = [];

  class FakeBrowserWindow extends EventEmitter {
    constructor(options) {
      super();
      this.options = options;
      this.destroyed = false;
      this.shown = false;
      this.focused = false;
      this.bounds = { x: 0, y: 0, width: 1050, height: 820 };
      this.loadedURLs = [];
      this.sentMessages = [];
      this.executedScripts = [];
      const webContents = new EventEmitter();
      webContents.id = windows.length + 1;
      webContents.mainFrame = { url: "" };
      webContents.setWindowOpenHandler = () => {};
      webContents.send = (channel, payload) => this.sentMessages.push({ channel, payload });
      webContents.getURL = () => this.loadedURLs.at(-1) || "";
      webContents.executeJavaScript = (code) => {
        this.executedScripts.push(code);
        return Promise.resolve();
      };
      this.webContents = webContents;
      windows.push(this);
    }

    isDestroyed() { return this.destroyed; }
    destroy() {
      this.destroyed = true;
      this.emit("closed");
    }
    show() { this.shown = true; }
    focus() { this.focused = true; }
    getBounds() { return this.bounds; }
    async loadURL(url) {
      this.loadedURLs.push(url);
      this.webContents.mainFrame.url = url;
    }
  }

  function makePartition(name) {
    if (!sessions.has(name)) {
      sessions.set(name, {
        name,
        setPermissionCheckHandler() {},
        setPermissionRequestHandler() {},
        setDisplayMediaRequestHandler() {},
        protocol: { handle() {} },
        webRequest: { onBeforeSendHeaders() {} },
        async clearStorageData(options) {
          clearedStorage.push({ partition: name, ...options });
        },
      });
    }
    return sessions.get(name);
  }

  const electron = {
    app: Object.assign(appEvents, {
      isPackaged: false,
      quit() {},
    }),
    BrowserWindow: FakeBrowserWindow,
    dialog: {
      showErrorBox(title, body) { errorBoxes.push({ title, body }); },
      async showMessageBox() { return { response: 1 }; },
    },
    ipcMain: {
      handle(channel, handler) { ipcHandlers.set(channel, handler); },
    },
    Menu: {
      buildFromTemplate(template) { return template; },
      setApplicationMenu() {},
    },
    session: {
      fromPartition: makePartition,
    },
    shell: {
      async openExternal(url) { externalURLs.push(url); },
    },
  };

  return { electron, windows, ipcHandlers, clearedStorage, externalURLs, errorBoxes };
}

test("start() restore and concurrent signIn() share the transition lock without double sign-in", async () => {
  const { electron, windows } = createFakeElectron();
  let resolveRestore;
  let restoreCalls = 0;
  let signInCalls = 0;

  const account = {
    accountID: () => "sub-1",
    async restore() {
      restoreCalls += 1;
      return new Promise((resolve) => { resolveRestore = resolve; });
    },
    async signIn() {
      signInCalls += 1;
    },
    async clear() {},
  };
  const home = {
    pairingInProgress: false,
    async retryRevocation() { return { revocationPending: false }; },
    async disconnect() { return { revocationPending: false }; },
  };
  const config = {
    webOrigin: "https://nori.example",
    browserOrigin: "https://nori.example",
    relayOrigin: "https://relay.example",
    googleClientId: "desktop.apps.googleusercontent.com",
    googleClientSecret: "secret",
    local: false,
  };

  const controller = createAppController({ electron, config, account, home });
  const starting = controller.start();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(controller.state.phase, "restoring");

  const signingIn = controller.signIn();
  resolveRestore(true);

  await Promise.all([starting, signingIn]);
  assert.equal(restoreCalls, 1);
  assert.equal(signInCalls, 0);
  assert.equal(controller.state.phase, "connected");
  assert.equal(controller.state.mode, "web");
  assert.equal(windows.at(-1).loadedURLs.at(-1), "https://nori.example");
  controller.dispose();
});

test("signOut clears origin localStorage via session API instead of executeJavaScript", async () => {
  const { electron, windows, clearedStorage } = createFakeElectron();
  let clearedAccount = 0;
  let disconnectedHome = 0;

  const account = {
    accountID: () => "sub-2",
    async restore() { return true; },
    async signIn() {},
    async clear() { clearedAccount += 1; },
  };
  const home = {
    pairingInProgress: false,
    async retryRevocation() { return { revocationPending: false }; },
    async disconnect() {
      disconnectedHome += 1;
      return { revocationPending: false };
    },
  };
  const config = {
    webOrigin: "https://nori.example",
    browserOrigin: "https://nori.example",
    relayOrigin: "https://relay.example",
    googleClientId: "desktop.apps.googleusercontent.com",
    googleClientSecret: "secret",
    local: false,
  };

  const controller = createAppController({ electron, config, account, home });
  await controller.start();
  assert.equal(controller.state.phase, "connected");
  const webWin = windows.at(-1);

  await controller.signOut();
  assert.equal(controller.state.phase, "welcome");
  assert.equal(controller.state.mode, "welcome");
  assert.equal(controller.state.resumeSession, false);
  assert.equal(clearedAccount, 1);
  assert.equal(disconnectedHome, 1);
  assert.equal(webWin.executedScripts.length, 0);
  assert.equal(clearedStorage.length, 1);
  assert.equal(clearedStorage[0].origin, "https://nori.example");
  assert.deepEqual(clearedStorage[0].storages, ["localstorage"]);
  controller.dispose();
});

test("signOut interrupting an in-flight restore keeps the app on welcome", async () => {
  const { electron } = createFakeElectron();
  let resolveRestore;
  const account = {
    accountID: () => "sub-3",
    async restore() {
      return new Promise((resolve) => { resolveRestore = resolve; });
    },
    async signIn() {},
    async clear() {},
  };
  const home = {
    pairingInProgress: false,
    async retryRevocation() { return { revocationPending: false }; },
    async disconnect() { return { revocationPending: false }; },
  };
  const config = {
    webOrigin: "https://nori.example",
    browserOrigin: "https://nori.example",
    relayOrigin: "https://relay.example",
    googleClientId: "desktop.apps.googleusercontent.com",
    googleClientSecret: "secret",
    local: false,
  };

  const controller = createAppController({ electron, config, account, home });
  const restoring = controller.start();
  await new Promise((resolve) => setImmediate(resolve));
  const signingOut = controller.signOut();
  resolveRestore(true);

  await Promise.all([restoring, signingOut]);
  assert.equal(controller.state.phase, "welcome");
  assert.equal(controller.state.mode, "welcome");
  controller.dispose();
});

test("expired session during restore signs out and reports welcome error", async () => {
  const { electron } = createFakeElectron();
  let cleared = 0;
  const account = {
    accountID: () => "sub-4",
    async restore() { throw new SignInError("Your Google session expired.", true); },
    async signIn() {},
    async clear() { cleared += 1; },
  };
  const home = {
    pairingInProgress: false,
    async retryRevocation() { return { revocationPending: false }; },
    async disconnect() { return { revocationPending: false }; },
  };
  const config = {
    webOrigin: "https://nori.example",
    browserOrigin: "https://nori.example",
    relayOrigin: "https://relay.example",
    googleClientId: "desktop.apps.googleusercontent.com",
    googleClientSecret: "secret",
    local: false,
  };

  const controller = createAppController({ electron, config, account, home });
  await controller.start();
  assert.equal(cleared, 1);
  assert.equal(controller.state.phase, "welcome");
  assert.equal(controller.welcomeStatus().error, true);
  assert.match(controller.welcomeStatus().message, /expired/i);
  controller.dispose();
});
