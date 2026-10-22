// StrataOS website: menu button on small screens, copy buttons on code
// blocks, and the search (an index loaded on first use, see docs-site.mjs).
(function () {
  "use strict";
  var fr = document.documentElement.lang === "fr";
  var T = fr
    ? { copy: "Copier", copied: "Copié", none: "Aucun résultat", results: "{0} résultat(s)" }
    : { copy: "Copy", copied: "Copied", none: "No results", results: "{0} result(s)" };

  // Menu
  var btn = document.querySelector(".menu-btn");
  if (btn) {
    btn.addEventListener("click", function () {
      var open = document.documentElement.classList.toggle("nav-open");
      btn.setAttribute("aria-expanded", String(open));
    });
  }

  // Copy buttons
  if (navigator.clipboard) {
    document.querySelectorAll("pre > code").forEach(function (code) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "copy";
      b.textContent = T.copy;
      b.addEventListener("click", function () {
        navigator.clipboard.writeText(code.innerText.replace(/\n$/, "")).then(function () {
          b.textContent = T.copied;
          b.classList.add("done");
          setTimeout(function () { b.textContent = T.copy; b.classList.remove("done"); }, 1500);
        });
      });
      code.parentNode.appendChild(b);
    });
  }

  // Search
  var form = document.querySelector("form.search");
  if (!form) return;
  var input = form.querySelector("input");
  var box = form.querySelector(".results");
  var status = form.querySelector("[aria-live]");
  var data = null, loading = null;
  var norm = function (s) { return s.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase(); };

  function load() {
    if (data) return Promise.resolve();
    if (!loading) {
      loading = new Promise(function (done) {
        var s = document.createElement("script");
        s.src = form.dataset.index;
        s.onload = function () {
          data = (window.STRATA_SEARCH || []).map(function (d) { return { t: d.t, u: d.u, x: d.x, nt: norm(d.t), nx: norm(d.x) }; });
          done();
        };
        s.onerror = function () { data = []; done(); };
        document.head.appendChild(s);
      });
    }
    return loading;
  }

  function find(q) {
    var terms = norm(q).split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    var hits = [];
    data.forEach(function (d) {
      var score = 0;
      for (var i = 0; i < terms.length; i++) {
        var inTitle = d.nt.indexOf(terms[i]) >= 0;
        if (!inTitle && d.nx.indexOf(terms[i]) < 0) return;
        score += inTitle ? 10 : 1;
      }
      hits.push({ d: d, score: score, terms: terms });
    });
    hits.sort(function (a, b) { return b.score - a.score; });
    return hits.slice(0, 12);
  }

  // Appends text to el with the search terms wrapped in <mark>.
  function highlight(el, text, terms) {
    var n = norm(text), i = 0;
    while (i < text.length) {
      var best = -1, len = 0;
      terms.forEach(function (t) {
        var j = n.indexOf(t, i);
        if (j >= 0 && (best < 0 || j < best)) { best = j; len = t.length; }
      });
      if (best < 0) { el.appendChild(document.createTextNode(text.slice(i))); break; }
      el.appendChild(document.createTextNode(text.slice(i, best)));
      var m = document.createElement("mark");
      m.textContent = text.slice(best, best + len);
      el.appendChild(m);
      i = best + len;
    }
  }

  function snippet(d, terms) {
    var at = -1;
    terms.forEach(function (t) { var j = d.nx.indexOf(t); if (j >= 0 && (at < 0 || j < at)) at = j; });
    if (at < 0) return d.x.slice(0, 140) + (d.x.length > 140 ? "…" : "");
    var start = Math.max(0, at - 50);
    return (start > 0 ? "…" : "") + d.x.slice(start, start + 160) + (start + 160 < d.x.length ? "…" : "");
  }

  function show(q) {
    var hits = find(q);
    box.textContent = "";
    if (!q.trim()) { box.hidden = true; status.textContent = ""; return; }
    if (!hits.length) {
      var li = document.createElement("li");
      li.className = "empty";
      li.textContent = T.none;
      box.appendChild(li);
    }
    hits.forEach(function (h) {
      var li = document.createElement("li");
      var a = document.createElement("a");
      a.href = form.dataset.base + h.d.u;
      var s = document.createElement("strong");
      highlight(s, h.d.t, h.terms);
      var p = document.createElement("span");
      highlight(p, snippet(h.d, h.terms), h.terms);
      a.appendChild(s);
      a.appendChild(p);
      li.appendChild(a);
      box.appendChild(li);
    });
    box.hidden = false;
    status.textContent = hits.length ? T.results.replace("{0}", hits.length) : T.none;
  }

  input.addEventListener("focus", load);
  input.addEventListener("input", function () { load().then(function () { show(input.value); }); });
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    load().then(function () {
      var first = box.querySelector("a");
      if (first) location.href = first.href;
    });
  });
  form.addEventListener("keydown", function (e) {
    var links = Array.prototype.slice.call(box.querySelectorAll("a"));
    var i = links.indexOf(document.activeElement);
    if (e.key === "ArrowDown" && links.length) { e.preventDefault(); links[Math.min(i + 1, links.length - 1)].focus(); }
    else if (e.key === "ArrowUp" && links.length) { e.preventDefault(); (i <= 0 ? input : links[i - 1]).focus(); }
    else if (e.key === "Escape") { box.hidden = true; input.focus(); }
  });
  document.addEventListener("click", function (e) { if (!form.contains(e.target)) box.hidden = true; });
  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement === document.body) { e.preventDefault(); input.focus(); }
  });
})();
