// Nori push notifications. Shows what the core sends ({title, body, url, topic}) and
// opens the linked screen in this app when tapped. Only same-origin paths are opened.
self.addEventListener("push", (event) => {
  let message = {};
  try { message = event.data ? event.data.json() : {}; } catch { message = { body: event.data ? event.data.text() : "" }; }
  const title = message.title || "Nori";
  event.waitUntil(self.registration.showNotification(title, {
    body: message.body || "",
    tag: message.topic || undefined,
    data: { url: typeof message.url === "string" ? message.url : "/" },
    icon: "/favicon.ico",
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  let target = "/";
  try {
    const url = new URL(event.notification.data && event.notification.data.url || "/", self.location.origin);
    if (url.origin === self.location.origin) target = url.pathname + url.search + url.hash;
  } catch { /* fall back to the app */ }
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const open = windows.find((client) => new URL(client.url).origin === self.location.origin);
    if (open) { await open.focus(); return open.navigate(target); }
    return self.clients.openWindow(target);
  })());
});
