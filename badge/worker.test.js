import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  renderBadge,
  unknownBadge,
  pickColor,
  pickLabel,
  handleGetBadge,
  handlePostBadge,
} from "./worker.js";

describe("pickColor", () => {
  it("returns green for 0 stale", () => {
    expect(pickColor(0)).toBe("#4c1");
  });
  it("returns yellow for 1-3 stale", () => {
    expect(pickColor(1)).toBe("#dfb317");
    expect(pickColor(3)).toBe("#dfb317");
  });
  it("returns red for 4+ stale", () => {
    expect(pickColor(4)).toBe("#e05d44");
    expect(pickColor(100)).toBe("#e05d44");
  });
});

describe("pickLabel", () => {
  it("returns Fresh for 0", () => {
    expect(pickLabel(0)).toBe("Fresh");
  });
  it("returns count for >0", () => {
    expect(pickLabel(5)).toBe("5 stale");
  });
});

describe("renderBadge", () => {
  it("returns valid SVG", () => {
    const svg = renderBadge("AI Context", "Fresh", "#4c1");
    expect(svg).toContain("<svg");
    expect(svg).toContain("AI Context");
    expect(svg).toContain("Fresh");
    expect(svg).toContain("#4c1");
  });
});

describe("unknownBadge", () => {
  it("renders grey unknown badge", () => {
    const svg = unknownBadge();
    expect(svg).toContain("unknown");
    expect(svg).toContain("#9f9f9f");
  });
});

// Mock KV store
function createMockKV(store = {}) {
  return {
    get: vi.fn(async (key, opts) => {
      const val = store[key];
      if (!val) return null;
      if (opts?.type === "json") return JSON.parse(val);
      return val;
    }),
    put: vi.fn(async (key, value) => {
      store[key] = value;
    }),
  };
}

function makeRequest(method, path, body, headers = {}) {
  const url = `https://badge.example.com${path}`;
  const init = { method, headers: new Headers(headers) };
  if (body) {
    init.body = JSON.stringify(body);
    init.headers.set("Content-Type", "application/json");
  }
  return new Request(url, init);
}

describe("handleGetBadge", () => {
  it("returns unknown badge when no data in KV", async () => {
    const kv = createMockKV();
    const env = { BADGE_KV: kv };
    const req = makeRequest("GET", "/badge/owner/repo.svg");
    const res = await handleGetBadge(req, env);

    expect(res.status).toBe(200);
    expect(res.headers.get("Content-Type")).toBe("image/svg+xml");
    const svg = await res.text();
    expect(svg).toContain("unknown");
  });

  it("returns green badge for 0 stale", async () => {
    const kv = createMockKV({
      "owner/repo": JSON.stringify({ total_stale: 0 }),
    });
    const env = { BADGE_KV: kv };
    const req = makeRequest("GET", "/badge/owner/repo.svg");
    const res = await handleGetBadge(req, env);

    const svg = await res.text();
    expect(svg).toContain("Fresh");
    expect(svg).toContain("#4c1");
  });

  it("returns red badge for 5 stale", async () => {
    const kv = createMockKV({
      "org/project": JSON.stringify({ total_stale: 5 }),
    });
    const env = { BADGE_KV: kv };
    const req = makeRequest("GET", "/badge/org/project.svg");
    const res = await handleGetBadge(req, env);

    const svg = await res.text();
    expect(svg).toContain("5 stale");
    expect(svg).toContain("#e05d44");
  });

  it("returns 404 for invalid path", async () => {
    const env = { BADGE_KV: createMockKV() };
    const req = makeRequest("GET", "/badge/invalid");
    const res = await handleGetBadge(req, env);
    expect(res.status).toBe(404);
  });
});

describe("handlePostBadge", () => {
  const TOKEN = "test-secret-token";

  it("rejects missing auth", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = makeRequest("POST", "/badge/owner/repo", { total_stale: 2 });
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(401);
  });

  it("rejects wrong token", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = makeRequest(
      "POST",
      "/badge/owner/repo",
      { total_stale: 2 },
      {
        Authorization: "Bearer wrong-token",
      },
    );
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(401);
  });

  it("stores data with valid auth", async () => {
    const store = {};
    const kv = createMockKV(store);
    const env = { BADGE_KV: kv, BADGE_API_TOKEN: TOKEN };
    const req = makeRequest(
      "POST",
      "/badge/owner/repo",
      {
        total_stale: 3,
        drift_score: 5,
        timestamp: "2026-04-01T00:00:00Z",
      },
      {
        Authorization: `Bearer ${TOKEN}`,
      },
    );

    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(200);

    const body = await res.json();
    expect(body.ok).toBe(true);

    expect(kv.put).toHaveBeenCalledOnce();
    const putArgs = kv.put.mock.calls[0];
    expect(putArgs[0]).toBe("owner/repo");
    const stored = JSON.parse(putArgs[1]);
    expect(stored.total_stale).toBe(3);
    expect(stored.drift_score).toBe(5);
  });

  it("rejects invalid total_stale", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = makeRequest(
      "POST",
      "/badge/owner/repo",
      { total_stale: -1 },
      {
        Authorization: `Bearer ${TOKEN}`,
      },
    );
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(400);
  });

  it("rejects non-JSON body", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = new Request("https://badge.example.com/badge/owner/repo", {
      method: "POST",
      headers: new Headers({ Authorization: `Bearer ${TOKEN}` }),
      body: "not json",
    });
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(400);
  });

  it("rejects invalid drift_score", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = makeRequest(
      "POST",
      "/badge/owner/repo",
      { total_stale: 0, drift_score: "abc" },
      { Authorization: `Bearer ${TOKEN}` },
    );
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(400);
  });

  it("rejects invalid timestamp", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = makeRequest(
      "POST",
      "/badge/owner/repo",
      { total_stale: 0, drift_score: 0, timestamp: "not-a-date" },
      { Authorization: `Bearer ${TOKEN}` },
    );
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(400);
  });

  it("returns 404 for invalid path", async () => {
    const env = { BADGE_KV: createMockKV(), BADGE_API_TOKEN: TOKEN };
    const req = makeRequest(
      "POST",
      "/badge/invalid",
      { total_stale: 0 },
      {
        Authorization: `Bearer ${TOKEN}`,
      },
    );
    const res = await handlePostBadge(req, env);
    expect(res.status).toBe(404);
  });
});
