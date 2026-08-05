// esc / fetchJSON / renderDiffSection / diffValueRow come from common.js.

fetchJSON(
  "/v1/" + encodeURIComponent(apiPrefix) + "/" + encodeURIComponent(chartName)
    + "/diff/" + encodeURIComponent(oldRef) + "/" + encodeURIComponent(newRef),
  render,
);

function render(data) {
  document.getElementById("loading").hidden = true;
  document.getElementById("content").hidden = false;

  renderRefHeader(data);
  renderImages(data);
  renderDiffSection("chart-yaml", data.chartYamlDiff);
  renderDiffSection("values", data.valuesDiff);
}

// renderRefHeader shows digest, chart version, and app version.
// Matching rows render once; differing rows render red→green.
function renderRefHeader(data) {
  const sec = document.getElementById("ref-meta");
  const from = data.from || {};
  const to = data.to || {};
  const digestValue = (s) => `<span class="digest">${esc(s)}</span>`;
  const plainValue = (s) => esc(s);
  const rows = [];
  const push = (label, f, t, fmt) => {
    const row = diffValueRow(label, f, t, fmt);
    if (row) rows.push(row);
  };
  push("Digest", from.digest, to.digest, digestValue);
  push("Chart version", from.chartVersion, to.chartVersion, plainValue);
  push("App version", from.appVersion, to.appVersion, plainValue);
  if (!rows.length) {
    sec.hidden = true;
    return;
  }
  sec.innerHTML = rows.join("");
}

// renderImages renders the added / removed / updated bucket, or a
// notice when the server signals fromImagesMissing / toImagesMissing.
function renderImages(data) {
  const section = document.getElementById("images-section");
  const el = document.getElementById("images");
  if (data.fromImagesMissing || data.toImagesMissing) {
    section.hidden = false;
    el.innerHTML = `<p class="empty">${imagesMissingNotice(data.fromImagesMissing, data.toImagesMissing)}</p>`;
    return;
  }
  if (!data.images) {
    section.hidden = true;
    return;
  }
  section.hidden = false;

  const added = data.images.added || [];
  const removed = data.images.removed || [];
  const updated = data.images.updated || [];
  if (!added.length && !removed.length && !updated.length) {
    el.innerHTML = '<p class="empty">No image changes.</p>';
    return;
  }

  const parts = [];
  if (updated.length) {
    parts.push("<h3>Updated (" + updated.length + ")</h3>");
    for (const u of updated) parts.push(updatedImageRow(u));
  }
  if (added.length) {
    parts.push("<h3>Added (" + added.length + ")</h3>");
    for (const a of added) parts.push(lockedImageRow(a, "added"));
  }
  if (removed.length) {
    parts.push("<h3>Removed (" + removed.length + ")</h3>");
    for (const r of removed) parts.push(lockedImageRow(r, "removed"));
  }
  el.innerHTML = parts.join("");
}

function imagesMissingNotice(fromMissing, toMissing) {
  if (fromMissing && toMissing) return "Neither chart carries image metadata; image changes can't be computed.";
  if (fromMissing) return "The 'from' chart carries no image metadata; image changes can't be computed.";
  return "The 'to' chart carries no image metadata; image changes can't be computed.";
}

function imagePathBreadcrumb(path) {
  if (!path || !path.length) return "";
  return `<span class="chart-subpath mono">${path.map(esc).join(" / ")}</span>`;
}

function requirementBadge(req) {
  if (!req) return "";
  return `<span class="chart-req chart-req-${esc(req)}">${esc(req)}</span>`;
}

function updatedImageRow(u) {
  return `<div class="row updated">
    <div class="chart-image-header">
      ${imagePathBreadcrumb(u.path)}
      <span class="name">${esc(u.logicalName)}</span>${requirementBadge(u.requirement)}
      ${updatedImageLink(u)}
    </div>
    <div class="chart-image-body mono">
      <span class="from-val">${esc(imageRef(u.fromRepoName, u.fromTag, u.fromDigest))}</span>
      <span class="arrow">→</span>
      <span class="to-val">${esc(imageRef(u.toRepoName, u.toTag, u.toDigest))}</span>
    </div>
  </div>`;
}

// updatedImageLink returns the cross-link to the image diff. Skipped
// on repo rename (two different images) and when neither side has a
// digest or tag to reference. Prefers digests, falls back to tags.
function updatedImageLink(u) {
  if (u.fromRepoName !== u.toRepoName) return "";
  const fromRef = u.fromDigest || u.fromTag;
  const toRef = u.toDigest || u.toTag;
  if (!fromRef || !toRef) return "";
  const href = "/repo/" + u.toRepoName.split("/").map(encodeURIComponent).join("/")
    + "/diff/" + encodeURIComponent(fromRef)
    + "/" + encodeURIComponent(toRef);
  return `<a class="compare-link" href="${esc(href)}" target="_blank" rel="noopener">compare<span class="ext-arrow" aria-hidden="true">↗</span></a>`;
}

function lockedImageRow(img, kind) {
  return `<div class="row ${esc(kind)}">
    <div class="chart-image-header">
      ${imagePathBreadcrumb(img.path)}
      <span class="name">${esc(img.logicalName)}</span>${requirementBadge(img.requirement)}
    </div>
    <div class="chart-image-body mono">${esc(imageRef(img.repoName, img.tag, img.digest))}</div>
  </div>`;
}

// imageRef formats an image as `repo:tag@sha256:xxxxxxx…`, omitting
// tag or digest components that are empty.
function imageRef(repo, tag, digest) {
  let out = repo || "";
  if (tag) out += ":" + tag;
  if (digest) out += "@" + shortDigest(digest);
  return out;
}

function shortDigest(digest) {
  if (!digest) return "";
  if (digest.startsWith("sha256:")) return digest.slice(0, 7 + 7);
  return digest;
}
