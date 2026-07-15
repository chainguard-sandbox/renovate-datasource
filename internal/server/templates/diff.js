// esc / ApiError / showError / fetchJSON / linked / repoLabel come from
// common.js, embedded before this script.

fetchJSON(
  "/v1/repo/" + repo.split("/").map(encodeURIComponent).join("/")
    + "/diff/" + encodeURIComponent(oldRef)
    + "/" + encodeURIComponent(newRef),
  render,
);

function render(data) {
  document.getElementById("loading").hidden = true;
  document.getElementById("content").hidden = false;

  renderRefHeader(data);

  // Build the set of "main" apk names — usually one, but if from and to
  // diverge we treat both as main so rows on either side stay highlighted.
  const mainSet = new Set();
  if (data.from.mainPackage) mainSet.add(data.from.mainPackage);
  if (data.to.mainPackage) mainSet.add(data.to.mainPackage);

  renderPackages(data.packages || {}, mainSet);
  renderSources(data.sources || {}, mainSet);
  renderDiffSection(
    "apko",
    data.apkoConfig,
    missingFileNotice("apko image-configuration attestation", data.from.digest, data.fromApkoMissing, data.to.digest, data.toApkoMissing),
  );
  renderConfig(data.config || []);
}

// renderRefHeader replaces the old side-by-side "From"/"To" ref-cards
// with a single metadata header matching the apk diff page: rows that
// match between sides render once, rows that differ render red→green.
// Uses diffValueRow / metaHeaderRow / formatTimestamp from common.js.
function renderRefHeader(data) {
  const sec = document.getElementById("ref-meta");
  const from = data.from || {};
  const to = data.to || {};
  const digestValue = (s) => `<span class="digest">${esc(s)}</span>`;
  const monoValue = (s) => `<span class="mono">${esc(s)}</span>`;
  const plainValue = (s) => esc(s);
  // .timestamp is already an ISO string from the backend, but we still
  // route it through formatTimestamp when it looks like a Unix epoch so
  // the two diff pages format consistently.
  const tsValue = (s) => {
    const n = Number(s);
    return `<span class="mono">${esc(Number.isFinite(n) && String(n) === s ? formatTimestamp(n) : s)}</span>`;
  };
  const rows = [];
  const push = (label, f, t, fmt) => {
    const row = diffValueRow(label, f, t, fmt);
    if (row) rows.push(row);
  };
  push("Digest", from.digest, to.digest, digestValue);
  push("Timestamp", from.timestamp, to.timestamp, tsValue);
  push("Platform", from.platform, to.platform, monoValue);
  push("Main package", from.mainPackage, to.mainPackage, plainValue);
  if (!rows.length) {
    sec.hidden = true;
    return;
  }
  sec.innerHTML = rows.join("");
}

// sortMainFirst returns a copy of items with main entries pulled to the top,
// preserving the relative order of everything else (Array.sort is stable
// in modern engines).
function sortMainFirst(items, isMain) {
  return [...items].sort((a, b) => {
    const am = isMain(a) ? 0 : 1;
    const bm = isMain(b) ? 0 : 1;
    return am - bm;
  });
}

function renderPackages(pkg, mainSet) {
  const sec = document.getElementById("packages");
  const parts = [];
  const isMain = (item) => mainSet.has(item.name);
  const u = sortMainFirst(pkg.updated || [], isMain);
  const a = sortMainFirst(pkg.added || [], isMain);
  const r = sortMainFirst(pkg.removed || [], isMain);
  const cls = (item, kind) => "row " + kind + (isMain(item) ? " main" : "");
  const badge = (item) => isMain(item) ? '<span class="main-badge">main</span>' : "";
  if (u.length) {
    parts.push("<h3>Updated (" + u.length + ")</h3>");
    for (const item of u) {
      parts.push(
        `<div class="${cls(item, 'updated')}">
          <span class="name">${esc(item.name)}</span>${badge(item)}
          <span class="mono"><span class="from-val">${apkVersionLink(item.name, item.from)}</span><span class="arrow">→</span><span class="to-val">${apkVersionLink(item.name, item.to)}</span></span>
          ${apkLink(item)}
        </div>`
      );
    }
  }
  if (a.length) {
    parts.push("<h3>Added (" + a.length + ")</h3>");
    for (const item of a) {
      parts.push(
        `<div class="${cls(item, 'added')}">
          <span class="name">${esc(item.name)}</span>${badge(item)}
          <span class="mono to-val">${apkVersionLink(item.name, item.version)}</span>
        </div>`
      );
    }
  }
  if (r.length) {
    parts.push("<h3>Removed (" + r.length + ")</h3>");
    for (const item of r) {
      parts.push(
        `<div class="${cls(item, 'removed')}">
          <span class="name">${esc(item.name)}</span>${badge(item)}
          <span class="mono from-val">${apkVersionLink(item.name, item.version)}</span>
        </div>`
      );
    }
  }
  sec.innerHTML = parts.length ? parts.join("") : '<p class="empty">No package changes.</p>';
}

// apkVersionLink wraps a version reference in a link to its
// single-version page, so users can drill into any from/to/added/
// removed version without first going through the diff view.
function apkVersionLink(name, version) {
  if (!name || !version) return esc(version || "");
  const href = "/apk/" + encodeURIComponent(name)
    + "/version/" + encodeURIComponent(version);
  return `<a href="${esc(href)}">${esc(version)}</a>`;
}

function renderSources(src, mainSet) {
  const sec = document.getElementById("sources");
  const parts = [];
  const isMain = (s) => (s.packages || []).some((p) => mainSet.has(p));
  const u = sortMainFirst(src.updated || [], isMain);
  const a = sortMainFirst(src.added || [], isMain);
  const r = sortMainFirst(src.removed || [], isMain);

  const cls = (s, kind) => "row " + kind + (isMain(s) ? " main" : "");
  const badge = (s) => isMain(s) ? '<span class="main-badge">main</span>' : "";

  const plainName = (s) => `${esc(s.host)}/${esc(s.name)}`;

  const compareLink = (s) => s.compareUrl
    ? `<a class="compare-link" href="${esc(s.compareUrl)}" target="_blank" rel="noopener">compare<span class="ext-arrow" aria-hidden="true">↗</span></a>`
    : "";

  const usedBy = (s, prefix) => {
    const list = s.packages || [];
    if (!list.length) return "";
    const rendered = list.map((p) => mainSet.has(p)
      ? `<strong>${esc(p)}</strong>`
      : esc(p));
    return `<div class="pkg-ref">${prefix} ${rendered.join(", ")}</div>`;
  };

  if (u.length) {
    parts.push("<h3>Updated (" + u.length + ")</h3>");
    for (const item of u) {
      parts.push(
        `<div class="${cls(item, 'updated')}">
          <span class="name">${plainName(item)}</span>${badge(item)}
          <span class="mono"><span class="from-val">${linked(item.from, item.fromUrl)}</span><span class="arrow">→</span><span class="to-val">${linked(item.to, item.toUrl)}</span></span>
          ${compareLink(item)}
          ${usedBy(item, "used by")}
        </div>`
      );
    }
  }
  if (a.length) {
    parts.push("<h3>Added (" + a.length + ")</h3>");
    for (const item of a) {
      parts.push(
        `<div class="${cls(item, 'added')}">
          <span class="name">${plainName(item)}</span>${badge(item)}
          <span class="mono to-val">${linked(item.version, item.url)}</span>
          ${usedBy(item, "used by")}
        </div>`
      );
    }
  }
  if (r.length) {
    parts.push("<h3>Removed (" + r.length + ")</h3>");
    for (const item of r) {
      parts.push(
        `<div class="${cls(item, 'removed')}">
          <span class="name">${plainName(item)}</span>${badge(item)}
          <span class="mono from-val">${linked(item.version, item.url)}</span>
          ${usedBy(item, "was used by")}
        </div>`
      );
    }
  }
  sec.innerHTML = parts.length ? parts.join("") : '<p class="empty">No source changes.</p>';
}

function renderConfig(items) {
  const sec = document.getElementById("config");
  if (!items.length) {
    sec.innerHTML = '<p class="empty">No config changes.</p>';
    return;
  }
  const parts = [];
  for (const c of items) {
    let body = "";
    if (c.type === "changed") {
      body = `<span class="from-val">${esc(c.from)}</span><span class="arrow">→</span><span class="to-val">${esc(c.to)}</span>`;
    } else if (c.type === "added") {
      body = `<span class="to-val">${esc(c.to)}</span>`;
    } else if (c.type === "removed") {
      body = `<span class="from-val">${esc(c.from)}</span>`;
    }
    parts.push(
      `<div class="row ${esc(c.type)}">
        <code class="name">${esc(c.field)}</code>
        <span class="mono">${body}</span>
      </div>`
    );
  }
  sec.innerHTML = parts.join("");
}

// apkLink renders a small link affordance next to each updated
// package row that points at the per-package diff page. The URL is the
// symmetric snapshot-pair shape so it's structurally identical to a
// cross-package diff (name repeated twice for the same-name case).
function apkLink(item) {
  const enc = encodeURIComponent;
  const href = "/apk/" + enc(item.name) + "/version/" + enc(item.from)
    + "/diff/" + enc(item.name) + "/version/" + enc(item.to);
  return `<a class="apk-link" href="${esc(href)}">compare</a>`;
}

