// Cloudflare Worker — Livedocs Badge Endpoint
// GET  /badge/:owner/:repo.svg  → SVG badge
// POST /badge/:owner/:repo      → update cached results (requires auth)

const COLORS = {
  green: "#4c1",
  yellow: "#dfb317",
  red: "#e05d44",
  grey: "#9f9f9f",
};

function escapeXml(str) {
  return String(str)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function pickColor(totalStale) {
  if (totalStale === 0) return COLORS.green;
  if (totalStale <= 3) return COLORS.yellow;
  return COLORS.red;
}

function pickLabel(totalStale) {
  if (totalStale === 0) return "Fresh";
  return `${totalStale} stale`;
}

function renderBadge(label, message, color) {
  const safeLabel = escapeXml(label);
  const safeMessage = escapeXml(message);
  const labelWidth = label.length * 6.5 + 10;
  const messageWidth = message.length * 6.5 + 10;
  const totalWidth = labelWidth + messageWidth;

  return `<svg xmlns="http://www.w3.org/2000/svg" width="${totalWidth}" height="20" role="img" aria-label="${safeLabel}: ${safeMessage}">
  <title>${safeLabel}: ${safeMessage}</title>
  <linearGradient id="s" x2="0" y2="100%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <clipPath id="r">
    <rect width="${totalWidth}" height="20" rx="3" fill="#fff"/>
  </clipPath>
  <g clip-path="url(#r)">
    <rect width="${labelWidth}" height="20" fill="#555"/>
    <rect x="${labelWidth}" width="${messageWidth}" height="20" fill="${color}"/>
    <rect width="${totalWidth}" height="20" fill="url(#s)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="11">
    <text aria-hidden="true" x="${labelWidth / 2}" y="15" fill="#010101" fill-opacity=".3">${safeLabel}</text>
    <text x="${labelWidth / 2}" y="14">${safeLabel}</text>
    <text aria-hidden="true" x="${labelWidth + messageWidth / 2}" y="15" fill="#010101" fill-opacity=".3">${safeMessage}</text>
    <text x="${labelWidth + messageWidth / 2}" y="14">${safeMessage}</text>
  </g>
</svg>`;
}

function unknownBadge() {
  return renderBadge("AI Context", "unknown", COLORS.grey);
}

async function handleGetBadge(request, env) {
  const url = new URL(request.url);
  const match = url.pathname.match(/^\/badge\/([^/]+)\/([^/]+)\.svg$/);
  if (!match) {
    return new Response("Not found", { status: 404 });
  }

  const [, owner, repo] = match;
  const key = `${owner}/${repo}`;
  const data = await env.BADGE_KV.get(key, { type: "json" });

  let svg;
  if (!data) {
    svg = unknownBadge();
  } else {
    const message = pickLabel(data.total_stale);
    const color = pickColor(data.total_stale);
    svg = renderBadge("AI Context", message, color);
  }

  return new Response(svg, {
    headers: {
      "Content-Type": "image/svg+xml",
      "Cache-Control": "public, max-age=300, s-maxage=300",
      "Content-Security-Policy": "default-src 'none'",
      "Access-Control-Allow-Origin": "*",
    },
  });
}

function timingSafeEqual(a, b) {
  const encoder = new TextEncoder();
  const bufA = encoder.encode(a);
  const bufB = encoder.encode(b);
  if (bufA.byteLength !== bufB.byteLength) return false;
  // crypto.subtle.timingSafeEqual is available in Cloudflare Workers
  if (typeof crypto !== "undefined" && crypto.subtle?.timingSafeEqual) {
    return crypto.subtle.timingSafeEqual(bufA, bufB);
  }
  // Fallback: constant-time compare
  let result = 0;
  for (let i = 0; i < bufA.byteLength; i++) {
    result |= bufA[i] ^ bufB[i];
  }
  return result === 0;
}

async function handlePostBadge(request, env) {
  const authHeader = request.headers.get("Authorization");
  const expected = `Bearer ${env.BADGE_API_TOKEN}`;
  if (!authHeader || !timingSafeEqual(authHeader, expected)) {
    return new Response("Unauthorized", { status: 401 });
  }

  const url = new URL(request.url);
  const match = url.pathname.match(/^\/badge\/([^/]+)\/([^/]+)$/);
  if (!match) {
    return new Response("Not found", { status: 404 });
  }

  let body;
  try {
    body = await request.json();
  } catch {
    return new Response("Invalid JSON", { status: 400 });
  }

  const totalStale = Number(body.total_stale);
  if (!Number.isFinite(totalStale) || totalStale < 0) {
    return new Response("Invalid total_stale value", { status: 400 });
  }

  const driftScore = Number(body.drift_score);
  if (!Number.isFinite(driftScore) || driftScore < 0) {
    return new Response("Invalid drift_score value", { status: 400 });
  }

  let timestamp = new Date().toISOString();
  if (body.timestamp) {
    const parsed = Date.parse(body.timestamp);
    if (!Number.isFinite(parsed)) {
      return new Response("Invalid timestamp value", { status: 400 });
    }
    timestamp = new Date(parsed).toISOString();
  }

  const [, owner, repo] = match;
  const key = `${owner}/${repo}`;
  const record = {
    owner,
    repo,
    total_stale: totalStale,
    drift_score: driftScore,
    timestamp,
  };

  await env.BADGE_KV.put(key, JSON.stringify(record), {
    expirationTtl: 86400 * 30, // 30 days
  });

  return new Response(JSON.stringify({ ok: true }), {
    headers: { "Content-Type": "application/json" },
  });
}

export default {
  async fetch(request, env) {
    if (request.method === "GET") {
      return handleGetBadge(request, env);
    }
    if (request.method === "POST") {
      return handlePostBadge(request, env);
    }
    return new Response("Method not allowed", { status: 405 });
  },
};

// Exported for testing
export {
  renderBadge,
  unknownBadge,
  pickColor,
  pickLabel,
  handleGetBadge,
  handlePostBadge,
};
