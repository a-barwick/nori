"use strict";

const { contextBridge, ipcRenderer } = require("electron");

if (process.isMainFrame) {
  contextBridge.exposeInMainWorld("noriWelcome", {
    signIn: () => ipcRenderer.invoke("nori:welcome-sign-in"),
    status: () => ipcRenderer.invoke("nori:welcome-status"),
    subscribe: (listener) => { ipcRenderer.on("nori:welcome-changed", (_event, value) => listener(value)); },
  });
}
