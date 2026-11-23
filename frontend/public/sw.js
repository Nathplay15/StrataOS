// Service worker of the web app: installable on iPhone/iPad (add to the Home
// Screen) and desktop browsers. It only caches the shell (interface, icons):
// the NAS data always comes from the network, even offline, never from a
// stale cache.
const SHELL = "strata-shell";
const SHELL_FILES = ["/", "/logo.svg", "/manifest.webmanifest", "/icon-192.png", "/icon-512.png", "/icon-maskable-512.png", "/apple-touch-icon.png"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(SHELL_FILES)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== "GET" || url.origin !== location.origin) return;
  // The API and the live data are never cached.
  if (url.pathname.startsWith("/api/")) return;
  // Vite hashes its assets: what is in /assets/ never changes, serve the
  // cache first, the network fills it in.
  if (url.pathname.startsWith("/assets/")) {
    e.respondWith(
      caches.match(e.request).then(
        (hit) =>
          hit ??
          fetch(e.request).then((res) => {
            if (res.ok) {
              const copy = res.clone();
              caches.open(SHELL).then((c) => c.put(e.request, copy));
            }
            return res;
          }),
      ),
    );
    return;
  }
  // The shell itself (/, /index.html): network first, cache when offline.
  e.respondWith(
    fetch(e.request)
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(SHELL).then((c) => c.put(e.request, copy));
        }
        return res;
      })
      .catch(() => caches.match(e.request).then((hit) => hit ?? caches.match("/"))),
  );
});

// Web push notifications of the NAS (the payload is the one the mobile app
// receives: {type:"notification", subject, message, link, ...}).
self.addEventListener("push", (e) => {
  let d = {};
  try { d = e.data.json(); } catch { d = { subject: "StrataOS", message: e.data ? e.data.text() : "" }; }
  if (d.type && d.type !== "notification") return;
  e.waitUntil(
    self.registration.showNotification(d.subject || "StrataOS", {
      body: d.message || "",
      icon: "/icon-192.png",
      badge: "/icon-maskable-512.png",
      tag: "strata-" + (d.host || "") + "-" + (d.id ?? Date.now()),
      renotify: true,
      data: { link: d.link || "/" },
    }),
  );
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const link = (e.notification.data && e.notification.data.link) || "/";
  e.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((cs) => {
      for (const c of cs) {
        if ("focus" in c) {
          c.focus();
          if (link !== "/" && "navigate" in c) c.navigate(link);
          return;
        }
      }
      return self.clients.openWindow(link);
    }),
  );
});
