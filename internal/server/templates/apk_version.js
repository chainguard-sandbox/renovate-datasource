// esc / ApiError / showError / fetchJSON / linked / repoLabel come from
// common.js, embedded before this script.

fetchJSON(
  "/v1/apk/" + encodeURIComponent(apkName)
    + "/version/" + encodeURIComponent(version),
  render,
);

function render(data) {
  document.getElementById("loading").hidden = true;
  document.getElementById("content").hidden = false;

  renderMetaHeader(data);
  renderSourceList(data);
  renderText("melange", data.melange, "No .melange.yaml in this apk.");
  renderText("pkginfo", data.pkginfo, "No .PKGINFO in this apk.");
}

// renderMetaHeader renders the URL and the structured .PKGINFO fields
// (arch, size, description, build date, license) as an aligned
// key/value header. Fields absent from the response are silently
// skipped so older apks without .PKGINFO fall back to just the URL.
function renderMetaHeader(data) {
  const sec = document.getElementById("apk-meta");
  const rows = [];
  if (data.url) {
    const link = `<a class="mono apk-meta-url" href="${esc(data.url)}" target="_blank" rel="noopener">${esc(data.url)}</a>`;
    rows.push(metaHeaderRow("URL", link));
  }
  const md = data.metadata || {};
  if (md.description) rows.push(metaHeaderRow("Description", esc(md.description)));
  if (md.arch)        rows.push(metaHeaderRow("Arch", `<span class="mono">${esc(md.arch)}</span>`));
  if (md.size)        rows.push(metaHeaderRow("Size", `<span class="mono">${esc(formatBytes(md.size))}</span>`));
  if (md.buildDate)   rows.push(metaHeaderRow("Built", `<span class="mono">${esc(formatTimestamp(md.buildDate))}</span>`));
  if (md.license)     rows.push(metaHeaderRow("License", `<span class="mono">${esc(md.license)}</span>`));
  if (!rows.length) {
    sec.hidden = true;
    return;
  }
  sec.innerHTML = rows.join("");
}

// renderSourceList renders a flat list of git-checkout and fetch
// entries as neutral ref-cards (no diff coloring). Distinct name from
// apk.js's renderSources (which renders a diff) to avoid confusion.
// Without a .melange.yaml there are no pipelines to parse; we say so
// explicitly instead of falling through to "No sources declared.",
// which would suggest the apk built without any.
function renderSourceList(data) {
  const sec = document.getElementById("sources");
  if (!data.melange) {
    sec.innerHTML = '<p class="empty">Source pipelines unavailable: no .melange.yaml in this apk.</p>';
    return;
  }
  const git = data.gitCheckouts || [];
  const fetches = data.fetches || [];
  if (!git.length && !fetches.length) {
    sec.innerHTML = '<p class="empty">No sources declared.</p>';
    return;
  }
  const parts = [];
  for (const g of git) {
    parts.push(gitCard(g));
  }
  for (const f of fetches) {
    parts.push(fetchCard(f));
  }
  sec.innerHTML = parts.join("");
}

function gitCard(g) {
  // We only emit a homepage hyperlink when the backend resolved a safe
  // (http/https) host+path pair; otherwise show the raw repository
  // string as plain text to avoid surfacing javascript:/data: links.
  const hasSafeHomepage = g.host && g.path;
  const homepage = hasSafeHomepage ? `https://${g.host}/${g.path}` : "";
  const urlLabel = hasSafeHomepage ? `${g.host}/${g.path}` : (g.repository || "—");
  const tagCell = g.tag
    ? linked(g.tag, g.tagUrl)
    : (g.branch ? esc(g.branch) : "—");
  const commitCell = g.commit
    ? linked(g.commit.slice(0, 12), g.commitUrl)
    : "—";
  return `<div class="ref-card source-card">
    <h3>Git checkout</h3>
    <div class="ref-field"><span class="ref-label">URL</span><span class="ref-val mono">${linked(urlLabel, homepage)}</span></div>
    <div class="ref-field"><span class="ref-label">Tag</span><span class="ref-val mono">${tagCell}</span></div>
    <div class="ref-field"><span class="ref-label">Commit</span><span class="ref-val mono">${commitCell}</span></div>
  </div>`;
}

function fetchCard(f) {
  return `<div class="ref-card source-card">
    <h3>Fetch</h3>
    <div class="ref-field"><span class="ref-label">URI</span><span class="ref-val mono">${esc(f.uri || "—")}</span></div>
    <div class="ref-field"><span class="ref-label">Hash</span><span class="ref-val mono">${esc(f.hash || "—")}</span></div>
  </div>`;
}

// renderText shows raw text content in a <pre>. Used for both the
// .melange.yaml and .PKGINFO sections on the version page. The class
// is `raw` rather than `diff` because we're not rendering unified-diff
// markup here — just monospace text in a similarly styled block.
// emptyNotice is the message shown in place of the file when the apk
// didn't include it (older packages predate melange.yaml embedding).
function renderText(id, text, emptyNotice) {
  const sec = document.getElementById(id);
  if (!text) {
    sec.innerHTML = `<p class="empty">${emptyNotice || "Not available."}</p>`;
    return;
  }
  sec.innerHTML = `<pre class="raw">${esc(text)}</pre>`;
}

