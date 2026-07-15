// esc / ApiError / showError / fetchJSON / linked / repoLabel come from
// common.js, embedded before this script. apiURL, fromVer, toVer are
// injected inline by the template so this script stays agnostic about
// which side of the symmetric diff URL it's serving.

fetchJSON(apiURL, render);

function render(data) {
  document.getElementById("loading").hidden = true;
  document.getElementById("content").hidden = false;

  renderDiffMetaHeader(data);
  renderSources(data);
  renderDiffSection(
    "melange",
    data.melange,
    missingFileNotice(".melange.yaml", data.from, data.fromMelangeMissing, data.to, data.toMelangeMissing),
  );
  renderDiffSection(
    "pkginfo",
    data.pkginfo,
    missingFileNotice(".PKGINFO", data.from, data.fromPkginfoMissing, data.to, data.toPkginfoMissing),
  );
}

// renderDiffMetaHeader renders the URL + .PKGINFO metadata block from
// both versions. Fields that match between from and to render once
// (neutral); fields that differ render with the from-val/to-val
// red→green treatment used elsewhere on the diff pages. Fields absent
// from one side but present in the other render as an added
// (green-only) or removed (red-only) value so it's clear which side
// gained/lost the entry.
function renderDiffMetaHeader(data) {
  const sec = document.getElementById("apk-meta");
  const from = data.fromMetadata || {};
  const to = data.toMetadata || {};
  const rows = [];
  const urlValue = (u) => u ? `<a class="mono apk-meta-url" href="${esc(u)}" target="_blank" rel="noopener">${esc(u)}</a>` : "";
  const monoValue = (s) => s ? `<span class="mono">${esc(s)}</span>` : "";
  const plainValue = (s) => s ? esc(s) : "";
  const sizeValue = (n) => n ? `<span class="mono">${esc(formatBytes(n))}</span>` : "";
  const tsValue = (n) => n ? `<span class="mono">${esc(formatTimestamp(n))}</span>` : "";
  const push = (label, fromVal, toVal, fmt) => {
    const row = diffValueRow(label, fromVal, toVal, fmt);
    if (row) rows.push(row);
  };
  push("URL", data.fromUrl, data.toUrl, urlValue);
  push("Description", from.description, to.description, plainValue);
  push("Arch", from.arch, to.arch, monoValue);
  push("Size", from.size, to.size, sizeValue);
  push("Built", from.buildDate, to.buildDate, tsValue);
  push("License", from.license, to.license, monoValue);
  if (!rows.length) {
    sec.hidden = true;
    return;
  }
  sec.innerHTML = rows.join("");
}


// renderSources fills the source-pipelines section with rows for each
// git-checkout and fetch change. Mirrors the image diff's "Upstream
// source changes" so the two pages feel consistent. Local var is
// `fetches` (not `fetch`) so we don't shadow window.fetch.
//
// When either side's apk lacks a .melange.yaml we surface that
// directly — without the yaml the parsed pipelines are empty by
// definition and the empty-state "No source changes." would be
// misleading.
function renderSources(data) {
  const sec = document.getElementById("sources");
  const sourcesNotice = missingFileNotice(".melange.yaml", data.from, data.fromMelangeMissing, data.to, data.toMelangeMissing);
  if (sourcesNotice) {
    sec.innerHTML = `<p class="empty">${sourcesNotice}</p>`;
    return;
  }
  const s = data.sources;
  const git = s && s.gitCheckouts;
  const fetches = s && s.fetches;
  const empty = (!git || !(git.added.length + git.removed.length + git.updated.length))
             && (!fetches || !(fetches.added.length + fetches.removed.length + fetches.updated.length));
  if (empty) {
    sec.innerHTML = '<p class="empty">No source changes.</p>';
    return;
  }
  const parts = [];
  if (git) parts.push(renderGitCheckout(git));
  if (fetches) parts.push(renderFetches(fetches));
  sec.innerHTML = parts.join("");
}

// renderGitCheckout renders each git-checkout change as a ref-card
// matching the version page layout: URL / Tag / Commit fields, with a
// compare↗ pill in the card title for updated entries. The Tag and
// Commit values on an updated card are rendered as
// "<from-val>X</from-val> → <to-val>Y</to-val>", so the diff coloring
// shows up exactly on the two fields the user cares about. Added and
// removed cards color the whole Tag/Commit value green (added) or red
// (removed).
function renderGitCheckout(g) {
  const parts = [];
  if (g.updated.length) {
    parts.push("<h3>Git checkouts updated (" + g.updated.length + ")</h3>");
    for (const item of g.updated) {
      parts.push(updatedGitCard(item));
    }
  }
  if (g.added.length) {
    parts.push("<h3>Git checkouts added (" + g.added.length + ")</h3>");
    for (const item of g.added) {
      parts.push(sidedGitCard(item, "to"));
    }
  }
  if (g.removed.length) {
    parts.push("<h3>Git checkouts removed (" + g.removed.length + ")</h3>");
    for (const item of g.removed) {
      parts.push(sidedGitCard(item, "from"));
    }
  }
  return parts.join("");
}

function updatedGitCard(item) {
  return `<div class="ref-card source-card">
    <div class="source-card-title">
      <h3>Git checkout</h3>
      ${compareLink(item)}
    </div>
    ${gitURLField(item)}
    <div class="ref-field">
      <span class="ref-label">Tag</span>
      <span class="ref-val mono"><span class="from-val">${gitTagValue(item.from)}</span><span class="arrow">→</span><span class="to-val">${gitTagValue(item.to)}</span></span>
    </div>
    <div class="ref-field">
      <span class="ref-label">Commit</span>
      <span class="ref-val mono"><span class="from-val">${gitCommitValue(item.from)}</span><span class="arrow">→</span><span class="to-val">${gitCommitValue(item.to)}</span></span>
    </div>
  </div>`;
}

// sidedGitCard renders a single-state card (added or removed) — the
// side modifier ("from" red, "to" green) colors the Tag and Commit
// values to match the diff convention.
function sidedGitCard(item, side) {
  const colorClass = side === "from" ? "from-val" : "to-val";
  return `<div class="ref-card source-card">
    <h3>Git checkout</h3>
    ${gitURLField(item)}
    <div class="ref-field"><span class="ref-label">Tag</span><span class="ref-val mono ${colorClass}">${gitTagValue(item)}</span></div>
    <div class="ref-field"><span class="ref-label">Commit</span><span class="ref-val mono ${colorClass}">${gitCommitValue(item)}</span></div>
  </div>`;
}

function gitURLField(item) {
  const hasSafeHomepage = item.host && item.path;
  const homepage = hasSafeHomepage ? `https://${item.host}/${item.path}` : "";
  const urlLabel = hasSafeHomepage ? `${item.host}/${item.path}` : (item.repository || "—");
  return `<div class="ref-field"><span class="ref-label">URL</span><span class="ref-val mono">${linked(urlLabel, homepage)}</span></div>`;
}

function gitTagValue(s) {
  if (s.tag) return linked(s.tag, s.tagUrl);
  if (s.branch) return esc(s.branch);
  return "—";
}

function gitCommitValue(s) {
  if (s.commit) return linked(s.commit.slice(0, 12), s.commitUrl);
  return "—";
}

// renderFetches surfaces fetch-pipeline changes. Updated entries render
// as side-by-side from/to ref-cards mirroring the image diff's ref
// section, since fetch URIs are too long to fit comfortably on a single
// row. Added/removed entries render as a single full-width card.
function renderFetches(f) {
  const parts = [];
  if (f.updated.length) {
    parts.push("<h3>Fetches updated (" + f.updated.length + ")</h3>");
    for (const item of f.updated) {
      parts.push(`<div class="refs source-refs">
        ${fetchCard("From", "from", item.fromUri, item.fromHash)}
        ${fetchCard("To", "to", item.toUri, item.toHash)}
      </div>`);
    }
  }
  if (f.added.length) {
    parts.push("<h3>Fetches added (" + f.added.length + ")</h3>");
    for (const item of f.added) {
      parts.push(fetchCard("Added", "to", item.uri, item.hash));
    }
  }
  if (f.removed.length) {
    parts.push("<h3>Fetches removed (" + f.removed.length + ")</h3>");
    for (const item of f.removed) {
      parts.push(fetchCard("Removed", "from", item.uri, item.hash));
    }
  }
  return parts.join("");
}

// fetchCard renders one ref-card. side is the diff-side modifier
// ("from" → red header, "to" → green header) so added/removed inherit
// the same green/red sense as updated rows.
function fetchCard(label, side, uri, hash) {
  return `<div class="ref-card source-card ${esc(side)}">
    <h3>${esc(label)}</h3>
    <div class="ref-field"><span class="ref-label">URI</span><span class="ref-val mono">${esc(uri || "—")}</span></div>
    <div class="ref-field"><span class="ref-label">Hash</span><span class="ref-val mono">${esc(hash || "—")}</span></div>
  </div>`;
}

function compareLink(item) {
  if (!item.compareUrl) return "";
  return `<a class="compare-link" href="${esc(item.compareUrl)}" target="_blank" rel="noopener">compare<span class="ext-arrow" aria-hidden="true">↗</span></a>`;
}

