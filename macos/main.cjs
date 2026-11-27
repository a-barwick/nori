"use strict";

const { createHash } = require("node:crypto");
const { dirname, join } = require("node:path");
const { readFile } = require("node:fs/promises");
const { GoogleSession, SignInError } = require("./google-auth.cjs");
const { HomeConnector, nativeCommands } = require("./connector.cjs");
const { canCallDesktop, isSameOrigin, isSignOut, readConfig } = require("./policy.cjs");

const welcomeURL = "nori-app://welcome/";
const welcomeFiles = new Map([
  [welcomeURL, ["welcome.html", "text/html; charset=utf-8"]],
  [welcomeURL + "welcome.js", ["welcome.js", "text/javascript; charset=utf-8"]],
]);

function createAppController({
  electron,
  config,
  account,
  home,
  readAsset = readFile,
  revocationRetryDelayMs = 30_000,
}) {
  const { app, BrowserWindow, dialog, ipcMain, Menu, session, shell } = electron;

  const state = {
    phase: "welcome", // "welcome" | "restoring" | "signing_in" | "connected" | "signing_out"
    window: undefined,
    mode: undefined, // "welcome" | "web"
    activeWebContext: undefined,
    lastBounds: undefined,
    resumeSession: true,
    welcome: { busy: false, message: "", error: false },
    revocationTimer: undefined,
    welcomeProtocolRegistered: false,
    authTransition: undefined,
    signOutTransition: undefined,
  };

  function isConnected() {
    return state.phase === "connected";
  }

  function isSigningOut() {
    return Boolean(state.signOutTransition) || state.phase === "signing_out";
  }

  function welcomeStatus() {
    return {
      ...state.welcome,
      label: config.local ? "Continue to Nori" : "Sign in with Google",
      unconfigured: !config.local && !config.googleClientId,
      message: state.welcome.message || (!config.local && !config.googleClientId
        ? "Google sign-in is awaiting configuration for Nori for Mac." : ""),
    };
  }

  function updateWelcome(patch) {
    state.welcome = { ...state.welcome, ...patch };
    if (state.mode === "welcome" && state.window && !state.window.isDestroyed()) {
      state.window.webContents.send("nori:welcome-changed", welcomeStatus());
    }
  }

  function createWindow(kind, browserSession) {
    const bounds = state.window && !state.window.isDestroyed() ? state.window.getBounds() : state.lastBounds;
    const old = state.window;
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
    state.mode = kind;
    state.window = next;
    next.once("ready-to-show", () => { if (!next.isDestroyed()) next.show(); });
    next.on("close", () => { state.lastBounds = next.getBounds(); });
    next.on("closed", () => {
      if (state.window !== next) return;
      state.window = undefined;
      if (state.activeWebContext?.browser === browserSession) state.activeWebContext = undefined;
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
    if (state.mode === "welcome" && state.window && !state.window.isDestroyed()) {
      state.window.focus();
      return;
    }
    state.activeWebContext = undefined;
    const browser = session.fromPartition("nori-welcome");
    isolateBrowser(browser);
    if (!state.welcomeProtocolRegistered) {
      browser.protocol.handle("nori-app", async ({ method, url }) => {
        const asset = welcomeFiles.get(url);
        if (!asset || method !== "GET") return new Response(null, { status: 404 });
        const [file, type] = asset;
        try {
          return new Response(await readAsset(join(__dirname, file)), { headers: { "Content-Type": type } });
        } catch {
          return new Response(null, { status: 404 });
        }
      });
      state.welcomeProtocolRegistered = true;
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
      const { requestHeaders, url, initiatorOrigin, webContentsId, resourceType } = details;
      const requestURL = resourceType === "webSocket" ? url.replace(/^ws(s?):/i, "http$1:") : url;
      const sameOrigin = isSameOrigin(requestURL, config.webOrigin);
      if (!sameOrigin) {
        for (const header of Object.keys(requestHeaders)) {
          if (/^(authorization|proxy-authorization)$/i.test(header)) delete requestHeaders[header];
        }
        callback({ requestHeaders });
        return;
      }

      const initiatedByNori = !initiatorOrigin || isSameOrigin(initiatorOrigin, config.webOrigin);
      if (!isConnected() || isSigningOut() || !state.window || state.activeWebContext !== context ||
          (webContentsId && webContentsId !== state.window.webContents.id) || !initiatedByNori) {
        callback({ cancel: true });
        return;
      }
      if (config.local) {
        callback({ requestHeaders });
        return;
      }

      void account.token().then((token) => {
        if (!isConnected() || isSigningOut() || state.activeWebContext !== context || !state.window) {
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
        if (state.activeWebContext === context && error instanceof SignInError && error.sessionExpired) {
          void signOut().catch(showSignOutError);
        }
      });
    });
    return context;
  }

  function showWeb() {
    if (state.mode === "web" && state.window && !state.window.isDestroyed()) {
      state.window.focus();
      return;
    }
    state.activeWebContext = webSession();
    const next = createWindow("web", state.activeWebContext.browser);
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
    clearTimeout(state.revocationTimer);
    state.revocationTimer = undefined;
    if (!result?.revocationPending) return;
    state.revocationTimer = setTimeout(() => {
      void home.retryRevocation().then(scheduleRevocationRetry, () => scheduleRevocationRetry(result));
    }, revocationRetryDelayMs);
  }

  async function clearOriginStorage(browser) {
    if (!browser || typeof browser.clearStorageData !== "function") return;
    await browser.clearStorageData({
      origin: config.webOrigin,
      storages: ["localstorage"],
    }).catch(() => {});
  }

  async function signOut() {
    if (state.signOutTransition) return state.signOutTransition;
    const browserToClear = state.activeWebContext?.browser;
    state.phase = "signing_out";
    state.signOutTransition = (async () => {
      const result = await home.disconnect();
      await clearOriginStorage(browserToClear);
      if (!config.local) await account.clear();
      state.phase = "welcome";
      state.resumeSession = false;
      updateWelcome({
        error: false,
        message: result.revocationPending
          ? "Sharing stopped on this Mac. Keep Nori open or reopen it when you’re online to finish disconnecting."
          : "",
      });
      scheduleRevocationRetry(result);
      showWelcome();
    })();
    try {
      await state.signOutTransition;
    } finally {
      state.signOutTransition = undefined;
      if (state.phase === "signing_out") state.phase = "welcome";
    }
  }

  async function restoreSession() {
    if (config.local || !config.googleClientId) return false;
    if (state.authTransition) {
      await state.authTransition;
      return isConnected();
    }
    const transition = (async () => {
      if (state.signOutTransition) await state.signOutTransition;
      if (isConnected()) return true;
      state.phase = "restoring";
      updateWelcome({ busy: true, error: false, message: "" });
      try {
        const restored = await account.restore();
        if (state.phase !== "restoring") return false;
        if (restored) {
          state.phase = "connected";
          showWeb();
          return true;
        }
        state.phase = "welcome";
        return false;
      } catch (error) {
        if (state.phase === "restoring") state.phase = "welcome";
        if (error instanceof SignInError && error.sessionExpired) {
          await signOut().catch(showSignOutError);
        }
        updateWelcome({
          error: true,
          message: error instanceof SignInError ? error.message : "Nori couldn’t restore your sign-in.",
        });
        return false;
      } finally {
        updateWelcome({ busy: false });
      }
    })();
    state.authTransition = transition;
    try {
      return await transition;
    } finally {
      if (state.authTransition === transition) state.authTransition = undefined;
    }
  }

  async function signIn() {
    if (state.authTransition) {
      await state.authTransition;
      if (isConnected()) return;
    }
    if (state.authTransition) return state.authTransition;
    const transition = (async () => {
      updateWelcome({ busy: true, error: false, message: "" });
      try {
        if (state.signOutTransition) await state.signOutTransition;
        if (isConnected()) return;
        state.phase = "signing_in";
        if (!config.local && !(state.resumeSession && await account.restore())) {
          if (state.phase !== "signing_in") return;
          const result = await home.disconnect();
          scheduleRevocationRetry(result);
          if (state.phase !== "signing_in") return;
          await account.signIn((url) => {
            updateWelcome({ message: "Continue in your browser." });
            return shell.openExternal(url);
          });
        }
        if (state.phase !== "signing_in") return;
        state.phase = "connected";
        showWeb();
      } catch (error) {
        if (state.phase === "signing_in") state.phase = "welcome";
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
    state.authTransition = transition;
    try {
      await transition;
    } finally {
      if (state.authTransition === transition) state.authTransition = undefined;
    }
  }

  function currentMainFrame(event) {
    const current = state.window?.webContents;
    const frame = event.senderFrame;
    return current && event.sender === current && frame && frame === current.mainFrame;
  }

  function welcomeAction(channel, operation) {
    ipcMain.handle(channel, (event) => {
      if (state.mode !== "welcome" || !currentMainFrame(event) || event.senderFrame.url !== welcomeURL) {
        throw new Error("This window can’t use Nori sign-in.");
      }
      return operation();
    });
  }

  function webAction(channel, operation, homeOnly = true) {
    ipcMain.handle("nori:" + channel, (event) => {
      if (state.mode !== "web" || !isConnected() || isSigningOut() || !currentMainFrame(event) ||
          !canCallDesktop(event.senderFrame.url, config.webOrigin, homeOnly)) {
        throw new Error("This page can’t control Nori for Mac.");
      }
      return operation(event.senderFrame.url);
    });
  }

  async function registerBackground() {
    const openedFor = state.window;
    const status = await home.status();
    if (status.background === "enabled" || status.background === "approval_required") return status;
    const { response } = await dialog.showMessageBox(state.window, {
      type: "question",
      title: "Use this Mac as a Home AI Server?",
      message: "Allow Nori to run in the background?",
      detail: "When this Mac is awake and Ollama is running, your Nori account can use its models even after you quit Nori.",
      buttons: ["Allow", "Cancel"],
      defaultId: 0,
      cancelId: 1,
    });
    if (response !== 0) return status;
    if (isSigningOut() || !isConnected() || !state.window || state.window !== openedFor ||
        !canCallDesktop(state.window.webContents.getURL(), config.webOrigin, true)) {
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
        { label: "Sign Out", click: () => { if (isConnected()) void signOut().catch(showSignOutError); } },
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
    setupActions();
    configureMenu();

    app.on("activate", () => { if (isConnected()) showWeb(); else showWelcome(); });
    app.on("second-instance", () => { if (isConnected()) showWeb(); else showWelcome(); });
    app.on("window-all-closed", () => {});
    app.on("before-quit", (event) => {
      if (!home.pairingInProgress && !state.signOutTransition) return;
      event.preventDefault();
      void (state.signOutTransition || home.disconnect()).catch(() => {}).finally(() => app.quit());
    });
    void home.retryRevocation().then(scheduleRevocationRetry, () => {});
    showWelcome();
    await restoreSession();
  }

  function dispose() {
    clearTimeout(state.revocationTimer);
    state.revocationTimer = undefined;
  }

  return {
    state,
    start,
    restoreSession,
    signIn,
    signOut,
    welcomeStatus,
    dispose,
  };
}

if (require.main === module) {
  const electron = require("electron");
  const { app, dialog, protocol } = electron;

  app.setName("Nori");
  app.enableSandbox();
  protocol.registerSchemesAsPrivileged([{ scheme: "nori-app", privileges: { standard: true, secure: true } }]);

  if (!app.requestSingleInstanceLock()) {
    app.quit();
  } else {
    void app.whenReady().then(() => {
      const config = readConfig(require("./config.json"));
      const commands = nativeCommands(dirname(process.execPath), config);
      const account = new GoogleSession(config, commands.keychain);
      const home = new HomeConnector(commands);
      const controller = createAppController({ electron, config, account, home });
      return controller.start();
    }).catch(() => {
      dialog.showErrorBox("Couldn’t open Nori", "The Nori app is missing its service configuration. Reinstall Nori and try again.");
      app.quit();
    });
  }
}

module.exports = { createAppController };
