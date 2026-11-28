"use strict";

const { contextBridge, ipcRenderer } = require("electron");

if (process.isMainFrame) {
  if (process.platform === "darwin") {
    const applyDesktopAttr = () => {
      if (document.documentElement) {
        document.documentElement.dataset.desktop = "mac";
      }
    };
    applyDesktopAttr();
    window.addEventListener("DOMContentLoaded", applyDesktopAttr, { once: true });
    ipcRenderer.on("nori:fullscreen-changed", (_event, isFullScreen) => {
      if (!document.documentElement) return;
      if (isFullScreen) document.documentElement.dataset.fullscreen = "true";
      else delete document.documentElement.dataset.fullscreen;
    });
  }

  contextBridge.exposeInMainWorld("noriDesktop", {
    homeStatus: () => ipcRenderer.invoke("nori:home-status"),
    ollamaModels: () => ipcRenderer.invoke("nori:ollama-models"),
    registerBackground: () => ipcRenderer.invoke("nori:register-background"),
    openBackgroundSettings: () => ipcRenderer.invoke("nori:open-background-settings"),
    beginPairing: () => ipcRenderer.invoke("nori:begin-pairing"),
    finishPairing: () => ipcRenderer.invoke("nori:finish-pairing"),
    cancelPairing: () => ipcRenderer.invoke("nori:cancel-pairing"),
    disconnectHome: () => ipcRenderer.invoke("nori:disconnect-home"),
    signOut: () => ipcRenderer.invoke("nori:sign-out"),
    openInBrowser: () => ipcRenderer.invoke("nori:open-in-browser"),
  });
}
