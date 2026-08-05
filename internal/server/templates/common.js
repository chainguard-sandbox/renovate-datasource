// common.js — shared utilities embedded ahead of each page's own
// script. Provides:
//   esc(s)            HTML-escape a string for safe text/attribute use
//   ApiError          throwable carrying the upstream HTTP status
//   showError(err)    render an error block and hide the loading UI
//   fetchJSON(url, render)
//                     fetch + JSON-decode with consistent error mapping;
//                     calls render(data) on success or showError on
//                     failure
//   linked(value, url)
//                     hyperlink helper that opens external URLs in a new
//                     tab with the ↗ marker
//   repoLabel(item)   "host/path" when available, else the raw repo URL

function esc(s) {
  if (s == null) return "";
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;"
  }[c]));
}

class ApiError extends Error {
  constructor(message, status, statusText) {
    super(message);
    this.status = status;
    this.statusText = statusText;
  }
}

function showError(err) {
  document.getElementById("loading").hidden = true;
  const el = document.getElementById("error");
  el.innerHTML = "";
  const main = document.createElement("div");
  main.textContent = err.message;
  el.appendChild(main);
  if (err instanceof ApiError) {
    const sub = document.createElement("div");
    sub.className = "error-status";
    sub.textContent = `HTTP ${err.status} ${err.statusText}`;
    el.appendChild(sub);
  }
  el.hidden = false;
}

// fetchJSON wraps the standard fetch+parse+error flow that every page
// performs on load. Pages just hand in the API URL and a render
// function — the boilerplate around error decoding and showError lives
// here.
function fetchJSON(url, render) {
  fetch(url, { headers: { "Accept": "application/json" } })
    .then(async (r) => {
      if (!r.ok) {
        let message = `${r.status} ${r.statusText}`;
        try {
          const body = await r.json();
          if (body && body.error) message = body.error;
        } catch (_) { /* keep status fallback */ }
        throw new ApiError(message, r.status, r.statusText);
      }
      return r.json();
    })
    .then(render)
    .catch(showError);
}

// isSafeHTTPURL keeps only http(s) references; everything else is
// dropped rather than escaped. Defense in depth for any caller that
// forwards a URL from the network into an href.
function isSafeHTTPURL(u) {
  return typeof u === "string" && /^https?:\/\//i.test(u);
}

// linked emits an external hyperlink with the ↗ external-link
// indicator. Falls back to plain escaped text when the url is empty
// or isn't a plain http(s) reference — keeps a well-meaning caller
// from turning a `javascript:` string into an active link.
function linked(value, url) {
  if (!isSafeHTTPURL(url)) return esc(value);
  return `<a href="${esc(url)}" target="_blank" rel="noopener">${esc(value)}<span class="ext-arrow" aria-hidden="true">↗</span></a>`;
}

// repoLabel renders the human-readable "host/path" label for a parsed
// git-checkout entry, falling back to the raw repository field when
// host/path couldn't be derived (e.g. for unknown VCS hosts).
function repoLabel(item) {
  if (item.host && item.path) return item.host + "/" + item.path;
  return item.repository || "";
}

// renderDiffSection drops a styled <pre class="diff"> into the section
// matching `id`, classifying each line as add/rem/hunk/file/ctx so
// diff.css can color them. `notice` overrides the empty-state message
// (typical use: "X version doesn't have a Y file") when the diff text
// is empty for a reason other than byte-equality.
function renderDiffSection(id, text, notice) {
  const sec = document.getElementById(id);
  if (!text) {
    sec.innerHTML = `<p class="empty">${notice || "No differences."}</p>`;
    return;
  }
  const lines = text.split("\n");
  if (lines.length && lines[lines.length - 1] === "") lines.pop();
  const rendered = lines.map((line) => {
    let cls = "ctx";
    if (line.startsWith("+++") || line.startsWith("---")) cls = "file";
    else if (line.startsWith("@@")) cls = "hunk";
    else if (line.startsWith("+")) cls = "add";
    else if (line.startsWith("-")) cls = "rem";
    return `<span class="diff-line diff-${cls}">${esc(line)}</span>`;
  }).join("\n");
  sec.innerHTML = `<pre class="diff">${rendered}</pre>`;
}

// formatBytes renders a byte count in the largest unit that keeps the
// number ≥ 1 (KB / MB / GB), matching how people typically think of
// apk sizes. SI (1000) not binary (1024) — friendlier to non-technical
// readers and consistent with how registry UIs report sizes.
function formatBytes(n) {
  if (n < 1000) return n + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1000;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
  return v.toFixed(v >= 100 ? 0 : v >= 10 ? 1 : 2) + " " + units[i];
}

// formatTimestamp converts a Unix-seconds epoch (as reported by
// .PKGINFO's builddate) into an ISO 8601 UTC string — timezone-neutral
// and unambiguous, which we prefer over locale-formatted strings on a
// page that anyone in any timezone might read.
function formatTimestamp(unixSeconds) {
  const d = new Date(unixSeconds * 1000);
  return d.toISOString().replace(/\.\d+Z$/, "Z");
}

// metaHeaderRow emits a single label/value row for the metadata
// header. Structured as a ref-field so it inherits the Sources box's
// label/value styling and column widths.
function metaHeaderRow(label, valueHTML) {
  return `<div class="ref-field"><span class="ref-label">${esc(label)}</span><span class="ref-val">${valueHTML}</span></div>`;
}

// diffValueRow renders one label/value row for a from→to metadata
// diff. Equality is checked on the *formatted* HTML so two inputs
// that round to the same display string (16.1 MB, 16.1 MB) collapse
// to a single row instead of a redundant red→green diff. Values only
// present on one side render red (from-only) or green (to-only).
// Returns null when both sides are empty so callers can skip the row.
function diffValueRow(label, fromRaw, toRaw, fmt) {
  const hasFrom = !!fromRaw;
  const hasTo = !!toRaw;
  if (!hasFrom && !hasTo) return null;
  const fromHTML = hasFrom ? fmt(fromRaw) : "";
  const toHTML = hasTo ? fmt(toRaw) : "";
  if (hasFrom && hasTo && fromHTML === toHTML) {
    return metaHeaderRow(label, toHTML);
  }
  if (hasFrom && !hasTo) {
    return metaHeaderRow(label, `<span class="from-val">${fromHTML}</span>`);
  }
  if (!hasFrom && hasTo) {
    return metaHeaderRow(label, `<span class="to-val">${toHTML}</span>`);
  }
  return metaHeaderRow(
    label,
    `<span class="from-val">${fromHTML}</span><span class="arrow">→</span><span class="to-val">${toHTML}</span>`,
  );
}

// missingFileNotice composes the placeholder shown when one or both
// sides of a diff didn't carry the file. Returns null when both sides
// had it (so the caller falls back to "No differences.").
function missingFileNotice(file, fromVer, fromMissing, toVer, toMissing) {
  if (!fromMissing && !toMissing) return null;
  if (fromMissing && toMissing) {
    return `No ${file} in either ${esc(fromVer)} or ${esc(toVer)} — nothing to compare.`;
  }
  const missingVer = fromMissing ? fromVer : toVer;
  return `No ${file} in ${esc(missingVer)} — nothing to compare.`;
}
