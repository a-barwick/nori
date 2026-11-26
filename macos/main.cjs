"use strict";

const { app, BrowserWindow, dialog, ipcMain, Menu, protocol, session, shell } = require("electron");
const { createHash } = require("node:crypto");
const { dirname, join } = require("node:path");
const { readFile } = require("node:fs/promises");
const { GoogleSession, SignInError } = require("./google-auth.cjs");
const { HomeConnector, nativeCommands } = require("./connector.cjs");
const { canCallDesktop, isSameOrigin, isSignOut, readConfig } = require("./policy.cjs");

app.setName("Nori");
app.enableSandbox();
protocol.registerSchemesAsPrivileged([{ scheme: "nori-app", privileges: { standard: true, secure: true } }]);

const welcomeURL = "nori-app://welcome/";
const welcomeFiles = new Map([
  [welcomeURL, ["welcome.html", "text/html; charset=utf-8"]],
  [welcomeURL + "welcome.js", ["welcome.js", "text/javascript; charset=utf-8"]],
]);
let config;
let account;
let home;
let window;
let mode;
let activeWebContext;
let connected = false;
let signingOut;
let signingIn;
let lastBounds;
let resumeSession = true;
let welcomeState = { busy: false, message: "", error: false };
let revocationTimer;
let welcomeProtocolRegistered = false;

function welcomeStatus() {
  return {
    ...welcomeState,
    label: config.local ? "Continue to Nori" : "Sign in with Google",
    unconfigured: !config.local && !config.googleClientId,
    message: welcomeState.message || (!config.local && !config.googleClientId
      ? "Google sign-in is awaiting configuration for Nori for Mac." : ""),
  };
}

function updateWelcome(state) {
  welcomeState = { ...welcomeState, ...state };
  if (mode === "welcome" && window && !window.isDestroyed()) {
    window.webContents.send("nori:welcome-changed", welcomeStatus());
  }
}

function createWindow(kind, browserSession) {
  const bounds = window && !window.isDestroyed() ? window.getBounds() : lastBounds;
  const old = window;
  const next = new BrowserWindow({
    width: 1050,
    height: 820,
    ...(bounds ?? {}),
    minWidth: 420,
    minHeight: 480,
    title: "Nori",
    show: false,
    webPreferences: {
      session: browserSession,
      preload: join(__dirname, kind === "web" ? "preload.cjs" : "welcome-preload.cjs"),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      webviewTag: false,
      devTools: !app.isPackaged,
    },
  });
  mode = kind;
  window = next;
  next.once("ready-to-show", () => { if (!next.isDestroyed()) next.show(); });
  next.on("close", () => { lastBounds = next.getBounds(); });
  next.on("closed", () => {
    if (window !== next) return;
    window = undefined;
    if (activeWebContext?.browser === browserSession) activeWebContext = undefined;
  });
  next.webContents.on("will-attach-webview", (event) => event.preventDefault());
  if (old && !old.isDestroyed()) old.destroy();
  return next;
}

function isolateBrowser(browser) {
  browser.setPermissionCheckHandler(() => false);
  browser.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  browser.setDisplayMediaRequestHandler((_request, callback) => callback({}));
}

function showWelcome() {
  if (mode === "welcome" && window && !window.isDestroyed()) {
    window.focus();
    return;
  }
  activeWebContext = undefined;
  const browser = session.fromPartition("nori-welcome");
  isolateBrowser(browser);
  if (!welcomeProtocolRegistered) {
    browser.protocol.handle("nori-app", async ({ method, url }) => {
      const asset = welcomeFiles.get(url);
      if (!asset || method !== "GET") return new Response(null, { status: 404 });
      const [file, type] = asset;
      try {
        return new Response(await readFile(join(__dirname, file)), { headers: { "Content-Type": type } });
      } catch {
        return new Response(null, { status: 404 });
      }
    });
    welcomeProtocolRegistered = true;
  }
  const next = createWindow("welcome", browser);
  next.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  next.webContents.on("will-frame-navigate", (event) => event.preventDefault());
  next.webContents.on("will-redirect", (event) => event.preventDefault());
  void next.loadURL(welcomeURL).catch(() => {});
}

function openExternal(url) {
  try {
    const target = new URL(url);
    if (target.protocol === "https:" && !target.username && !target.password) {
      void shell.openExternal(target.href).catch(() => {});
    }
  } catch {
    // Ignore invalid links.
  }
}

function webSession() {
  const scope = createHash("sha256").update(JSON.stringify([
    config.webOrigin, config.local ? "loopback-development" : account.accountID(),
  ])).digest("hex");
  const browser = session.fromPartition("persist:nori-account-" + scope);
  const context = { browser };
  isolateBrowser(browser);
  browser.webRequest.onBeforeSendHeaders((details, callback) => {
    const { requestHeaders, url, initiatorOrigin, webContentsId } = details;
    const sameOrigin = isSameOrigin(url, config.webOrigin);
    if (!sameOrigin) {
      for (const header of Object.keys(requestHeaders)) {
        if (/^(authorization|proxy-authorization)$/i.test(header)) delete requestHeaders[header];
      }
      callback({ requestHeaders });
      return;
    }

    const initiatedByNori = !initiatorOrigin || isSameOrigin(initiatorOrigin, config.webOrigin);
    if (!connected || signingOut || !window || activeWebContext !== context ||
        (webContentsId && webContentsId !== window.webContents.id) || !initiatedByNori) {
      callback({ cancel: true });
      return;
    }
    if (config.local) {
      callback({ requestHeaders });
      return;
    }

    void account.token().then((token) => {
      if (!connected || signingOut || activeWebContext !== context || !window) {
        callback({ cancel: true });
        return;
      }
      for (const header of Object.keys(requestHeaders)) {
        if (/^(authorization|proxy-authorization)$/i.test(header)) delete requestHeaders[header];
      }
      requestHeaders.Authorization = `Bearer ${token}`;
      callback({ requestHeaders });
    }, (error) => {
      callback({ cancel: true });
      if (activeWebContext === context && error instanceof SignInError && error.sessionExpired) {
        void signOut().catch(showSignOutError);
      }
    });
  });
  return context;
}

function showWeb() {
  if (mode === "web" && window && !window.isDestroyed()) {
    window.focus();
    return;
  }
  activeWebContext = webSession();
  const next = createWindow("web", activeWebContext.browser);
  next.webContents.on("will-frame-navigate", (event) => {
    const { url, isMainFrame } = event;
    if (isSameOrigin(url, config.webOrigin)) {
      if (isMainFrame && isSignOut(url, config.webOrigin)) {
        event.preventDefault();
        void signOut().catch(showSignOutError);
      }
      return;
    }
    if (!isMainFrame && (url === "about:blank" || url.startsWith("blob:" + config.webOrigin + "/"))) return;
    event.preventDefault();
    if (isMainFrame) openExternal(url);
  });
  next.webContents.on("will-redirect", (event) => {
    if (!isSameOrigin(event.url, config.webOrigin) || isSignOut(event.url, config.webOrigin)) event.preventDefault();
  });
  next.webContents.setWindowOpenHandler(({ url }) => {
    if (isSignOut(url, config.webOrigin)) void signOut().catch(showSignOutError);
    else if (isSameOrigin(url, config.webOrigin)) void next.loadURL(url).catch(() => {});
    else openExternal(url);
    return { action: "deny" };
  });
  void next.loadURL(config.webOrigin).catch(() => {});
}

function showSignOutError() {
  dialog.showErrorBox("Couldn’t sign out", "Nori couldn’t turn off Home AI sharing. Try signing out again.");
}

function scheduleRevocationRetry(result) {
  clearTimeout(revocationTimer);
  if (!result.revocationPending) return;
  revocationTimer = setTimeout(() => {
    void home.retryRevocation().then(scheduleRevocationRetry, () => scheduleRevocationRetry(result));
  }, 30_000);
}

async function signOut() {
  if (signingOut) return signingOut;
  signingOut = (async () => {
    const result = await home.disconnect();
    if (mode === "web" && window && isSameOrigin(window.webContents.getURL(), config.webOrigin)) {
      const clearSetup = window.webContents.executeJavaScript(`
        for (const key of Object.keys(localStorage)) {
          if (key.startsWith("nori.desktop.home-setup:")) localStorage.removeItem(key);
        }
      `);
      await Promise.race([clearSetup, new Promise((resolve) => setTimeout(resolve, 250))]).catch(() => {});

    }
    if (!config.local) await account.clear();
    connected = false;
    resumeSession = false;
    updateWelcome({
      error: false,
      message: result.revocationPending ? "Sharing stopped on this Mac. Keep Nori open or reopen it when you’re online to finish disconnecting." : "",
    });
    scheduleRevocationRetry(result);
    showWelcome();
  })();
  try {
    await signingOut;
  } finally {
    signingOut = undefined;
  }
}

async function signIn() {
  if (signingIn) return signingIn;
  signingIn = (async () => {
    updateWelcome({ busy: true, error: false, message: "" });
    try {
      if (signingOut) await signingOut;
      if (!config.local && !(resumeSession && await account.restore())) {
        const result = await home.disconnect();
        scheduleRevocationRetry(result);
        await account.signIn((url) => shell.openExternal(url));
      }
      connected = true;
      showWeb();
    } catch (error) {
      if (error instanceof SignInError && error.sessionExpired) {
        await signOut().catch(showSignOutError);
      }
      updateWelcome({
        error: true,
        message: error instanceof SignInError ? error.message : "Nori couldn’t complete sign-in. Try again.",
      });
    } finally {
      updateWelcome({ busy: false });
    }
  })();
  try {
    await signingIn;
  } finally {
    signingIn = undefined;
  }
}

function currentMainFrame(event) {
  const current = window?.webContents;
  const frame = event.senderFrame;
  return current && event.sender === current && frame && frame === current.mainFrame;
}

function welcomeAction(channel, operation) {
  ipcMain.handle(channel, (event) => {
    if (mode !== "welcome" || !currentMainFrame(event) || event.senderFrame.url !== welcomeURL) {
      throw new Error("This window can’t use Nori sign-in.");
    }
    return operation();
  });
}

function webAction(channel, operation, homeOnly = true) {
  ipcMain.handle("nori:" + channel, (event) => {
    if (mode !== "web" || !connected || signingOut || !currentMainFrame(event) ||
        !canCallDesktop(event.senderFrame.url, config.webOrigin, homeOnly)) {
      throw new Error("This page can’t control Nori for Mac.");
    }
    return operation(event.senderFrame.url);
  });
}

async function registerBackground() {
  const openedFor = window;
  const status = await home.status();
  if (status.background === "enabled" || status.background === "approval_required") return status;
  const { response } = await dialog.showMessageBox(window, {
    type: "question",
    title: "Use this Mac as a Home AI Server?",
    message: "Allow Nori to run in the background?",
    detail: "When this Mac is awake and Ollama is running, your Nori account can use its models even after you quit Nori.",
    buttons: ["Allow", "Cancel"],
    defaultId: 0,
    cancelId: 1,
  });
  if (response !== 0) return status;
  if (signingOut || !connected || !window || window !== openedFor ||
      !canCallDesktop(window.webContents.getURL(), config.webOrigin, true)) {
    throw new Error("Return to Models to enable Home AI sharing.");
  }
  return home.register();
}

function setupActions() {
  welcomeAction("nori:welcome-status", welcomeStatus);
  welcomeAction("nori:welcome-sign-in", signIn);
  webAction("home-status", () => home.status());
  webAction("ollama-models", () => home.models());
  webAction("register-background", registerBackground);
  webAction("open-background-settings", () => home.openSettings());
  webAction("begin-pairing", () => home.beginPairing());
  webAction("finish-pairing", async () => {
    try {
      await home.finishPairing();
    } catch (error) {
      scheduleRevocationRetry(await home.disconnect());
      throw error;
    }
  });
  webAction("cancel-pairing", async () => {
    scheduleRevocationRetry(await home.disconnect());
  });
  webAction("disconnect-home", async () => {
    const result = await home.disconnect();
    scheduleRevocationRetry(result);
    return result;
  });
  webAction("sign-out", signOut, false);
  webAction("open-in-browser", (url) => {
    const target = new URL(config.browserOrigin);
    target.pathname = new URL(url).pathname;
    target.searchParams.set("nori_desktop_continue", "1");
    return shell.openExternal(target.href);
  }, false);
}

function configureMenu() {
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    { label: "Nori", submenu: [
      { role: "about" },
      { type: "separator" },
      { label: "Sign Out", click: () => { if (connected) void signOut().catch(showSignOutError); } },
      { type: "separator" },
      { role: "hide" }, { role: "hideOthers" }, { role: "unhide" },
      { type: "separator" },
      { role: "quit" },
    ] },
    { role: "editMenu" },
    { role: "viewMenu", submenu: [{ role: "reload" }, { role: "resetZoom" }, { role: "zoomIn" }, { role: "zoomOut" }, { role: "togglefullscreen" }] },
    { role: "windowMenu" },
  ]));
}

async function start() {
  config = readConfig(require("./config.json"));
  const commands = nativeCommands(dirname(process.execPath), config);
  account = new GoogleSession(config, commands.keychain);
  home = new HomeConnector(commands);
  setupActions();
  configureMenu();

  app.on("activate", () => { if (connected) showWeb(); else showWelcome(); });
  app.on("second-instance", () => { if (connected) showWeb(); else showWelcome(); });
  app.on("window-all-closed", () => {});
  app.on("before-quit", (event) => {
    if (!home.pairingInProgress && !signingOut) return;
    event.preventDefault();
    void (signingOut || home.disconnect()).catch(() => {}).finally(() => app.quit());
  });
  void home.retryRevocation().then(scheduleRevocationRetry, () => {});
  showWelcome();
  if (!config.local && config.googleClientId) {
    updateWelcome({ busy: true, error: false, message: "" });
    try {
      if (await account.restore()) {
        connected = true;
        showWeb();
      }
    } catch (error) {
      if (error instanceof SignInError && error.sessionExpired) await signOut().catch(showSignOutError);
      updateWelcome({ error: true, message: error instanceof SignInError ? error.message : "Nori couldn’t restore your sign-in." });
    } finally {
      updateWelcome({ busy: false });
    }
  }
}

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  void app.whenReady().then(start).catch(() => {
    dialog.showErrorBox("Couldn’t open Nori", "The Nori app is missing its service configuration. Reinstall Nori and try again.");
    app.quit();
  });
}
