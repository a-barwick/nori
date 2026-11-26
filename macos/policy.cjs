"use strict";

function parseOrigin(value) {
  const url = new URL(value);
  if (url.username || url.password || url.pathname !== "/" || url.search || url.hash ||
      !(url.protocol === "https:" || (url.protocol === "http:" && url.hostname === "127.0.0.1" && url.port))) {
    throw new Error("Nori requires an HTTPS service origin or a loopback development port.");
  }
  return url;
}

function readConfig(raw) {
  const web = parseOrigin(raw.webOrigin);
  const relay = parseOrigin(raw.relayOrigin);
  const browser = parseOrigin(raw.browserOrigin || raw.webOrigin);
  const local = web.protocol === "http:";
  if (local !== (relay.protocol === "http:") || local !== (browser.protocol === "http:")) {
    throw new Error("Nori Web and the Home AI relay must belong to the same build environment.");
  }
  const googleClientId = raw.googleClientId || "";
  const googleClientSecret = raw.googleClientSecret || "";
  if (typeof googleClientId !== "string" || (googleClientId && !/^[\w-]+\.apps\.googleusercontent\.com$/.test(googleClientId)) ||
      typeof googleClientSecret !== "string" || (local && (googleClientId || googleClientSecret))) {
    throw new Error("Invalid Google desktop client configuration.");
  }
  return { webOrigin: web.origin, browserOrigin: browser.origin, relayOrigin: relay.origin, googleClientId, googleClientSecret, local };
}

function isSameOrigin(value, origin) {
  if (!value) return false;
  try {
    const parsed = new URL(value);
    return (parsed.protocol === "https:" || parsed.protocol === "http:") &&
      parsed.origin === origin && !parsed.username && !parsed.password;
  } catch {
    return false;
  }
}

function canCallDesktop(frameURL, origin, homeOnly) {
  if (!isSameOrigin(frameURL, origin)) return false;
  return !homeOnly || new URL(frameURL).pathname.replace(/\/$/, "") === "/settings/models";
}

function isSignOut(url, origin) {
  return isSameOrigin(url, origin) && ["/api/auth/sign-out", "/signed-out"].includes(new URL(url).pathname);
}

module.exports = { canCallDesktop, isSameOrigin, isSignOut, readConfig };
