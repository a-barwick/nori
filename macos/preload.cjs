"use strict";

const { contextBridge, ipcRenderer } = require("electron");

if (process.isMainFrame) {
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
