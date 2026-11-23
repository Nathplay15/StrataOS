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
// receives: {type:"notification", subject, message, link, image?, clip?}).
// A surveillance event carries its picture (shown on the notification) and
// its clip: tapping the notification plays the clip.
self.addEventListener("push", (e) => {
  let d = {};
  try { d = e.data.json(); } catch { d = { subject: "StrataOS", message: e.data ? e.data.text() : "" }; }
  if (d.type && d.type !== "notification") return;
  e.waitUntil(
    self.registration.showNotification(d.subject || "StrataOS", {
      body: d.message || "",
      icon: "/icon-192.png",
      badge: "/icon-maskable-512.png",
      image: d.image || undefined,
      tag: "strata-" + (d.host || "") + "-" + (d.id ?? Date.now()),
      renotify: true,
      data: { link: d.link || "/", clip: d.clip || "" },
    }),
  );
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const data = e.notification.data || {};
  // The clip of a surveillance event wins over the page link.
  const link = data.clip || data.link || "/";
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

// Automatic photo upload of the PWA (roadmap 0.15): the folders picked in
// Photos (persistent directory handles) and their rules (destination folder,
// pause) live in IndexedDB. The periodic background sync sends the new
// photos/videos of each folder to its NAS folder and skips what was sent.
const PWA_DB = "strata-pwa";
const mediaRe = /\.(jpe?g|png|gif|webp|heic|heif|mp4|mov|webm|mkv|m4v)$/i;

function pwaDb() {
  return new Promise((res, rej) => {
    const rq = indexedDB.open(PWA_DB, 1);
    rq.onupgradeneeded = () => {
      rq.result.createObjectStore("handles");
      rq.result.createObjectStore("cfg");
    };
    rq.onsuccess = () => res(rq.result);
    rq.onerror = () => rej(rq.error);
  });
}
function idbGet(db, store, key) {
  return new Promise((res, rej) => {
    const rq = db.transaction(store).objectStore(store).get(key);
    rq.onsuccess = () => res(rq.result);
    rq.onerror = () => rej(rq.error);
  });
}
function idbPut(db, store, key, val) {
  return new Promise((res, rej) => {
    const rq = db.transaction(store, "readwrite").objectStore(store).put(val, key);
    rq.onsuccess = () => res();
    rq.onerror = () => rej(rq.error);
  });
}

// uploadNewPhotos sends what the folder of each rule gained since the last run.
async function uploadNewPhotos() {
  const db = await pwaDb();
  const cfg = (await idbGet(db, "cfg", "rules")) || { rules: [], seen: {} };
  let sent = 0, failed = 0;
  for (const r of cfg.rules) {
    if (!r || r.paused || !r.target) continue;
    const h = await idbGet(db, "handles", r.dir);
    if (!h) continue;
    let perm = "granted";
    if (h.queryPermission) perm = await h.queryPermission({ mode: "read" });
    if (perm !== "granted") continue;
    cfg.seen[r.dir] = cfg.seen[r.dir] || {};
    for await (const e of h.values()) {
      if (e.kind !== "file" || !mediaRe.test(e.name)) continue;
      const f = await e.getFile();
      const key = `${f.name}:${f.size}:${f.lastModified}`;
      if (cfg.seen[r.dir][key]) continue;
      try {
        const fd = new FormData();
        fd.append("file", f, f.name);
        const resp = await fetch(`/api/files/upload?path=${encodeURIComponent(r.target)}`, { method: "POST", headers: { "X-Strata": "1" }, body: fd });
        if (!resp.ok) throw new Error(String(resp.status));
        cfg.seen[r.dir][key] = 1;
        sent++;
      } catch {
        failed++;
      }
    }
  }
  await idbPut(db, "cfg", "rules", cfg);
  db.close();
  if (sent || failed) {
    await self.registration.showNotification("StrataOS", {
      body: sent ? `${sent} photo(s)/video(s) sent to the NAS` + (failed ? `, ${failed} failed` : "") : `${failed} photo upload(s) failed`,
      icon: "/icon-192.png",
      tag: "strata-upload",
    });
  }
  return { sent, failed };
}

self.addEventListener("periodicsync", (e) => {
  if (e.tag === "strata-photo-upload") e.waitUntil(uploadNewPhotos());
});
self.addEventListener("sync", (e) => {
  if (e.tag === "strata-photo-upload") e.waitUntil(uploadNewPhotos());
});
// The page asks for an immediate run ("Send new photos now" with a folder handle).
self.addEventListener("message", (e) => {
  const d = e.data || {};
  if (d.type === "upload-now") {
    e.waitUntil(uploadNewPhotos().then((r) => { if (e.source) e.source.postMessage({ type: "upload-done", sent: r.sent, failed: r.failed }); }));
  }
});
