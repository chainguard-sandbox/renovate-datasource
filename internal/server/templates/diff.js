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

  renderVulnerabilities(data.vulnerabilities);
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

// renderVulnerabilities renders the vulnerability delta. Missing
// data hides the section; empty data renders a "no changes" note.
function renderVulnerabilities(vulns) {
  const section = document.getElementById("vulnerabilities-section");
  if (!vulns) {
    section.hidden = true;
    return;
  }
  section.hidden = false;

  const introduced = sortBySeverity(vulns.added || []);
  const fixed = sortBySeverity(vulns.removed || []);
  const el = document.getElementById("vulnerabilities");
  if (!introduced.length && !fixed.length) {
    el.innerHTML = '<p class="empty">No vulnerability changes.</p>';
    return;
  }
  const parts = [];
  if (introduced.length) {
    parts.push("<h3>Introduced (" + introduced.length + ")</h3>");
    for (const v of introduced) {
      parts.push(vulnerabilityRow(v, "introduced"));
    }
  }
  if (fixed.length) {
    parts.push("<h3>Fixed (" + fixed.length + ")</h3>");
    for (const v of fixed) {
      parts.push(vulnerabilityRow(v, "fixed"));
    }
  }
  el.innerHTML = parts.join("");
}

// severityRank orders grype severities most→least urgent; unknown
// sorts to the bottom.
function severityRank(s) {
  switch ((s || "").toLowerCase()) {
    case "critical": return 0;
    case "high":     return 1;
    case "medium":   return 2;
    case "low":      return 3;
    case "negligible": return 4;
    default:         return 5;
  }
}

function sortBySeverity(cs) {
  const out = cs.slice();
  out.sort((a, b) => {
    const s = severityRank(a.severity) - severityRank(b.severity);
    if (s !== 0) return s;
    // Higher CVSS first within a severity; unscored sorts last.
    const av = a.cvss && typeof a.cvss.score === "number" ? a.cvss.score : -1;
    const bv = b.cvss && typeof b.cvss.score === "number" ? b.cvss.score : -1;
    if (av !== bv) return bv - av;
    return (a.id || "").localeCompare(b.id || "");
  });
  return out;
}

function vulnerabilityRow(v, kind) {
  const sev = (v.severity || "unknown").toLowerCase();
  const pkgs = v.packages || [];
  const link = vulnerabilityLink(v);
  const kev = v.kev ? '<span class="vuln-kev" title="CISA Known Exploited Vulnerability">KEV</span>' : "";
  const cvss = v.cvss ? `<span class="vuln-cvss" title="${esc(v.cvss.vector || "")}">CVSS ${esc(String(v.cvss.score))}</span>` : "";
  const summary = `<summary class="row vuln ${esc(kind)}">
    <span class="vuln-sev sev-${esc(sev)}">${esc(v.severity || "Unknown")}</span>
    ${link}${kev}${cvss}
    <span class="vuln-pkgs">${pkgs.map(vulnerabilityPkgTag).join("")}</span>
  </summary>`;
  return `<details class="vuln-details">${summary}${vulnerabilityDetailsBody(v)}</details>`;
}

// vulnerabilityPkgTag renders one affected package as
// `name=version [type]` for the collapsed summary line.
function vulnerabilityPkgTag(pkg) {
  const type = pkg.type ? `<span class="vuln-type">${esc(pkg.type)}</span>` : "";
  return `<span class="vuln-pkg mono">${esc(pkg.name || "")}=${esc(pkg.version || "")}</span>${type}`;
}

// vulnerabilityDetailsBody renders the expanded panel: description,
// affected packages, and reference URLs. Missing sections drop.
function vulnerabilityDetailsBody(v) {
  const parts = [];
  if (v.description) {
    parts.push(`<div class="vuln-desc">${esc(v.description)}</div>`);
  }
  const pkgs = v.packages || [];
  if (pkgs.length) {
    const rows = pkgs.map((p) => {
      const type = p.type ? `<span class="vuln-type">${esc(p.type)}</span>` : "";
      const fixes = (p.fixVersions || []).length
        ? (p.fixVersions).map((v) => `<span class="mono">${esc(v)}</span>`).join(", ")
        : "—";
      const state = p.fixState ? `<span class="vuln-fixstate vuln-fixstate-${esc((p.fixState || "").toLowerCase())}">${esc(p.fixState)}</span>` : "";
      return `<tr>
        <td class="mono">${esc(p.name || "")}</td>
        <td class="mono">${esc(p.version || "")}</td>
        <td>${type}</td>
        <td>${fixes}</td>
        <td>${state}</td>
      </tr>`;
    }).join("");
    parts.push(`<table class="vuln-pkg-table">
      <thead><tr><th>Package</th><th>Version</th><th>Type</th><th>Fixed in</th><th>Fix state</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`);
  }
  const urls = (v.urls || []).filter(isSafeHTTPURL);
  if (urls.length) {
    const items = urls.map((u) => `<li><a href="${esc(u)}" target="_blank" rel="noopener">${esc(u)}</a></li>`).join("");
    parts.push(`<div class="vuln-refs">
      <h4>References</h4>
      <ul>${items}</ul>
    </div>`);
  }
  return parts.length ? `<div class="vuln-body">${parts.join("")}</div>` : "";
}

// vulnerabilityLink picks the best URL for the ID, falling back to
// advisoryURLFor when no advisory refs were attached. Non-http(s)
// URLs are dropped to avoid `javascript:`-scheme XSS.
function vulnerabilityLink(v) {
  const id = v.id || "";
  const urls = (v.urls || []).filter(isSafeHTTPURL);
  const href = urls.length ? urls[0] : advisoryURLFor(id);
  return `<a class="vuln-id mono" href="${esc(href)}" target="_blank" rel="noopener">${esc(id)}</a>`;
}

// isSafeHTTPURL keeps only http(s) references; everything else is
// dropped rather than escaped.
function isSafeHTTPURL(u) {
  return typeof u === "string" && /^https?:\/\//i.test(u);
}

function advisoryURLFor(id) {
  if (id.startsWith("GHSA-")) {
    return "https://github.com/advisories/" + encodeURIComponent(id);
  }
  if (id.startsWith("CVE-")) {
    return "https://nvd.nist.gov/vuln/detail/" + encodeURIComponent(id);
  }
  return "";
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

