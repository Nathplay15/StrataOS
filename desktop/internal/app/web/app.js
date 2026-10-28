"use strict";
// Settings page of StrataOS Sync, served on 127.0.0.1 by the app itself.
let T = {};
let state = null;
let tab = localStorage.getItem("tab") || "status";
let certFp = "";
let browsePath = "";

const $ = (id) => document.getElementById(id);
const t = (id, ...args) => (T[id] ?? id).replace(/\{(\d+)\}/g, (_, i) => args[+i] ?? "");
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

async function api(method, path, body) {
  const r = await fetch(path, { method, headers: { "Content-Type": "application/json", "X-Strata-Sync": "1" }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) { const e = new Error(data.error || r.statusText); e.data = data; throw e; }
  return data;
}

function showError(e) {
  const box = $("error");
  if (!e) { box.hidden = true; return; }
  box.textContent = e.message || String(e);
  box.hidden = false;
}

async function run(btn, fn) {
  showError(null);
  if (btn) btn.disabled = true;
  try { await fn(); } catch (e) { showError(e); } finally { if (btn) btn.disabled = false; refresh(); }
}

function applyTexts() {
  document.querySelectorAll("[data-t]").forEach((el) => { el.textContent = t(el.dataset.t); });
  document.documentElement.lang = state?.lang || "en";
}

function setTab(name) {
  tab = name;
  localStorage.setItem("tab", name);
  document.querySelectorAll("#tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  document.querySelectorAll("main > section").forEach((s) => { s.hidden = s.id !== "tab-" + name; });
}

function size(n) {
  if (n < 1024) return n + " B";
  const u = ["KB", "MB", "GB", "TB"];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(n < 10 ? 1 : 0) + " " + u[i];
}

const when = (d) => d ? new Date(d).toLocaleString(state?.lang || undefined) : "—";

function badge(p) {
  switch (p.state) {
    case "syncing": return `<span class="badge busy">${esc(t("state.syncing"))}</span>`;
    case "paused": return `<span class="badge">${esc(t("state.paused"))}</span>`;
    case "error": return `<span class="badge err">${esc(t("state.error"))}</span>`;
    case "brake": return `<span class="badge warn">${esc(t("state.brake"))}</span>`;
  }
  return `<span class="badge ok">${esc(t("state.uptodate"))}</span>`;
}

function renderStatus() {
  const st = state.status;
  $("signedout").hidden = !st.signedOut;
  const pairs = st.pairs || [];
  $("nofolder").hidden = pairs.length > 0 || !state.paired;
  $("pairs-status").innerHTML = pairs.map((p) => {
    let html = `<div class="card"><div class="pair-title">${badge(p)} <span class="mono">${esc(p.local)}</span> <span class="arrow">⇄</span> <span class="mono">${esc(p.remote)}</span></div>`;
    if (p.state === "syncing" && p.pending) html += `<div class="muted">${esc(t("status.progress", p.done, p.pending))}</div>`;
    if (p.current) html += `<div class="muted small mono">${esc(p.current)}</div><div class="bar"><div style="width:${Math.round((p.progress || 0) * 100)}%"></div></div>`;
    if (p.error) html += `<div class="notice error">${esc(p.error)}</div>`;
    if (p.brake) {
      const b = p.brake;
      html += `<div class="notice warn"><p>${esc(t("brake.text", b.localDeletes + b.remoteDeletes, b.files))}</p>`;
      if (b.localDeletes) html += `<p class="small">${esc(t("brake.local", b.localDeletes))}</p>`;
      if (b.remoteDeletes) html += `<p class="small">${esc(t("brake.remote", b.remoteDeletes))}</p>`;
      html += `<div class="row"><button class="danger" data-brake="${esc(p.id)}" data-apply="1">${esc(t("brake.apply"))}</button><button data-brake="${esc(p.id)}" data-apply="0">${esc(t("brake.restore"))}</button></div></div>`;
    }
    html += `<div class="muted small">${esc(t("status.files", p.files))} · ${esc(t("status.last", when(p.lastSync)))}${p.conflicts ? " · " + esc(t("status.conflicts", p.conflicts)) : ""}</div>`;
    const sk = p.skipped || [];
    if (sk.length || p.links) {
      html += `<details><summary>${esc(t("status.skipped", sk.length + (p.links || 0)))}</summary><ul class="skipped">`;
      if (p.links) html += `<li>${esc(t("status.links", p.links))}</li>`;
      html += sk.map((s) => `<li><span class="mono">${esc(s.path)}</span>: ${esc(s.reason)}</li>`).join("") + "</ul></details>";
    }
    return html + "</div>";
  }).join("");
}

function renderFolders() {
  const pairs = state.config.pairs || [];
  $("pairs").innerHTML = pairs.map((p) => `<div class="card">
      <div class="pair-title"><span class="mono">${esc(p.local)}</span> <span class="arrow">⇄</span> <span class="mono">${esc(p.remote)}</span>${p.paused ? ` <span class="badge">${esc(t("state.paused"))}</span>` : ""}</div>
      <label><span>${esc(t("folders.exclude"))}</span><textarea rows="2" data-exclude="${esc(p.id)}" spellcheck="false">${esc((p.exclude || []).join("\n"))}</textarea></label>
      <div class="row">
        <button data-open="${esc(p.id)}">${esc(t("action.openfolder"))}</button>
        <button data-save-ex="${esc(p.id)}">${esc(t("folders.saveexclude"))}</button>
        <button data-toggle="${esc(p.id)}" data-paused="${p.paused ? 1 : 0}">${esc(t(p.paused ? "action.resume" : "action.pause"))}</button>
        <button class="danger" data-remove="${esc(p.id)}">${esc(t("folders.remove"))}</button>
      </div></div>`).join("");
}

function renderAccount() {
  const c = state.config;
  $("paired").hidden = !state.paired;
  $("pairing").hidden = state.paired;
  if (state.paired) {
    $("acc-nas").textContent = c.nasName || "—";
    $("acc-server").textContent = c.server;
    $("acc-user").textContent = c.user;
    $("acc-fp").textContent = c.fingerprint || t("account.cafp");
  }
}

let settingsShown = false;
function renderSettings() {
  if (settingsShown) return; // do not overwrite what is being typed
  settingsShown = true;
  const c = state.config;
  $("s-device").value = c.deviceName || "";
  $("s-up").value = c.upKBps || 0;
  $("s-down").value = c.downKBps || 0;
  $("s-ignore").value = (c.ignore ?? state.defaultIgnore).join("\n");
  $("s-autostart").checked = !!state.autostart;
  $("s-notify").checked = !c.noNotify;
  $("s-lang").value = c.language || "";
  $("about").textContent = t("about", state.version, state.dir);
}

function render() {
  applyTexts();
  const st = state.status;
  $("pause").textContent = t(st.paused ? "action.resumeall" : "action.pauseall");
  $("pause").hidden = $("syncnow").hidden = !state.paired;
  const pairs = st.pairs || [];
  const busy = pairs.filter((p) => p.state === "syncing").reduce((n, p) => n + Math.max(0, p.pending - p.done), 0);
  $("summary").textContent = !state.paired ? t("summary.notpaired") : st.signedOut ? t("summary.signedout") : st.paused ? t("state.paused")
    : pairs.some((p) => p.state === "error") ? t("summary.error") : busy ? t("summary.syncing", busy) : t("state.uptodate");
  renderStatus();
  renderFolders();
  renderAccount();
  renderSettings();
}

async function refresh() {
  try {
    const s = await api("GET", "/api/state");
    if (!state || s.lang !== state.lang) T = await api("GET", "/api/i18n?lang=" + s.lang);
    state = s;
    if (!state.paired && tab === "status" && !localStorage.getItem("visited")) setTab("account");
    localStorage.setItem("visited", "1");
    render();
  } catch (e) { showError(e); }
}

// ---- Events ----
$("tabs").addEventListener("click", (e) => { if (e.target.dataset.tab) setTab(e.target.dataset.tab); });
$("pause").onclick = (e) => run(e.target, () => api("POST", "/api/pause", { on: !state.status.paused }));
$("syncnow").onclick = (e) => run(e.target, () => api("POST", "/api/sync-now"));
$("open-web").onclick = (e) => run(e.target, () => api("POST", "/api/open", { what: "web" }));
$("signout").onclick = (e) => { if (confirm(t("account.signoutconfirm"))) run(e.target, () => api("POST", "/api/signout")); };

document.body.addEventListener("click", (e) => {
  const d = e.target.dataset || {};
  if (d.brake) run(e.target, () => api("POST", "/api/brake", { pair: d.brake, apply: d.apply === "1" }));
  if (d.open) run(e.target, () => api("POST", "/api/open", { what: "folder", id: d.open }));
  if (d.toggle) run(e.target, () => api("PUT", "/api/pairs/" + d.toggle, { paused: d.paused !== "1" }));
  if (d.saveEx) {
    const ex = document.querySelector(`[data-exclude="${d.saveEx}"]`).value.split("\n");
    run(e.target, () => api("PUT", "/api/pairs/" + d.saveEx, { exclude: ex }));
  }
  if (d.remove && confirm(t("folders.removeconfirm"))) run(e.target, () => api("DELETE", "/api/pairs/" + d.remove));
  if (d.found) { $("p-url").value = d.found; $("found").innerHTML = ""; }
  if (d.dir !== undefined && e.target.closest("#remote-browser")) browse(d.dir);
  if (d.pick !== undefined) { $("remote").value = d.pick; $("remote-browser").hidden = true; suggestLocal(d.pick); }
});

async function browse(p) {
  browsePath = p;
  const box = $("remote-browser");
  box.hidden = false;
  box.innerHTML = `<div class="muted">…</div>`;
  try {
    if (!p) {
      const shares = await api("GET", "/api/remote/shares");
      box.innerHTML = shares.map((s) => `<div data-dir="${esc(s.path)}">📁 <b>${esc(s.name)}</b> <span class="muted small">${esc(s.writable ? "" : t("folders.readonly"))}</span></div>`).join("")
        || `<div class="muted">${esc(t("folders.noshares"))}</div>`;
      return;
    }
    const list = await api("GET", "/api/remote/list?path=" + encodeURIComponent(p));
    const up = p.split("/").slice(0, -1).join("/");
    box.innerHTML = `<div class="row" style="cursor:default"><span class="mono grow">${esc(p)}</span><button data-pick="${esc(p)}" class="primary">${esc(t("folders.choosethis"))}</button></div>`
      + `<div data-dir="${esc(list.isShare ? "" : up)}">⬆ ..</div>`
      + list.dirs.map((d) => `<div data-dir="${esc(d.path)}">📁 ${esc(d.name)}</div>`).join("");
  } catch (e) { box.innerHTML = `<div class="notice error">${esc(e.message)}</div>`; }
}

function suggestLocal(remote) {
  if ($("local").value) return;
  $("local").value = state.home + state.sep + "StrataOS" + state.sep + remote.split("/").pop();
}

$("browse-remote").onclick = () => browse("");
$("browse-local").onclick = (e) => run(e.target, async () => {
  const r = await api("POST", "/api/pick-folder");
  if (r.path) $("local").value = r.path;
});
$("add-pair").onclick = (e) => run(e.target, async () => {
  await api("POST", "/api/pairs", { local: $("local").value, remote: $("remote").value });
  $("local").value = $("remote").value = "";
  setTab("status");
});

document.querySelectorAll("input[name=method]").forEach((r) => r.addEventListener("change", () => {
  const m = document.querySelector("input[name=method]:checked").value;
  $("m-code").hidden = m !== "code";
  $("m-password").hidden = m !== "password";
}));

$("discover").onclick = (e) => run(e.target, async () => {
  const l = await api("POST", "/api/discover");
  $("found").innerHTML = l.length ? l.map((n) => `<div data-found="${esc(n.url)}">🖧 <b>${esc(n.name)}</b> <span class="mono muted">${esc(n.url)}</span></div>`).join("")
    : `<div class="muted">${esc(t("pair.nonefound"))}</div>`;
});

$("p-link").addEventListener("input", () => { certFp = ""; $("cert").hidden = true; });
$("p-url").addEventListener("input", () => { certFp = ""; $("cert").hidden = true; });

$("pair").onclick = (e) => run(e.target, async () => {
  const link = $("p-link").value.trim();
  const method = document.querySelector("input[name=method]:checked").value;
  const body = { link, url: $("p-url").value.trim() };
  if (!link) {
    if (method === "code") body.code = $("p-code").value.trim();
    else { body.username = $("p-user").value.trim(); body.password = $("p-pass").value; body.otp = $("p-otp").value.trim(); }
  }
  // Trust on first use: the certificate is shown before the password or code is sent.
  const probe = await api("POST", "/api/probe", { url: body.url, link });
  if (!probe.trusted && probe.fingerprint !== probe.expected) {
    if (probe.expected) throw new Error(t("pair.certmismatch", probe.fingerprint));
    if (certFp !== probe.fingerprint || !$("cert-ok").checked) {
      certFp = probe.fingerprint;
      $("cert-fp").textContent = probe.fingerprint;
      $("cert-ok").checked = false;
      $("cert").hidden = false;
      throw new Error(t("pair.checkcert"));
    }
  }
  body.fingerprint = probe.trusted ? "" : probe.fingerprint;
  try {
    await api("POST", "/api/pair", body);
  } catch (err) {
    if (err.data && err.data.twoFactor) { $("otp-row").hidden = false; $("p-otp").focus(); }
    throw err;
  }
  $("p-pass").value = $("p-code").value = $("p-link").value = "";
  $("cert").hidden = true;
  setTab("folders");
});

$("save-settings").onclick = (e) => run(e.target, async () => {
  await api("POST", "/api/settings", {
    deviceName: $("s-device").value, upKBps: +$("s-up").value || 0, downKBps: +$("s-down").value || 0,
    ignore: $("s-ignore").value.split("\n"), autostart: $("s-autostart").checked, notify: $("s-notify").checked, language: $("s-lang").value,
  });
  settingsShown = false;
  state = null;
});

setTab(tab);
refresh();
setInterval(refresh, 1500);
