// Client ⇔ server wire contract. testdata/sdk_wire.json records requests
// as the Go client builds them and the answers parse's handlers gave them
// (parse/sdk_wire_fixture_test.go). replay serves those answers only to a
// request equal to the recorded one, so these tests hold the SDK to the
// server's requests and shapes rather than to its own types.

import { readFileSync } from "node:fs";
import { isDeepStrictEqual } from "node:util";
import { describe, expect, expectTypeOf, it } from "vitest";
import { type Client, createClient } from "./client.js";
// ApiError and the problem types come through the package's public entry points.
import { ApiError, NonFiniteNumberError, ResponseError, SchemaResourceError, SchemaValidationError, SchemaVersionError, UnencodableValueError } from "./core/index.js";
import type { FacetResult, ListResult } from "./filter/types.js";
import type { FieldError, Problem } from "./index.js";
import type { PatchInput, PatchResult } from "./patch/types.js";
import { type ResourceSchema, SCHEMA_VERSION } from "./schema.js";

interface WireItem {
  id: string;
  name: string;
  labels: Record<string, unknown>;
}

// The fixture's wire_stock rows, keyed (warehouse_id, sku).
interface WireStock {
  warehouse_id: string;
  sku: string;
  qty: number;
}

// A request as the server received it: every value of each query
// parameter, and the body as JSON.
interface WireRequest {
  method: string;
  path: string;
  query?: Record<string, string[]>;
  content_type?: string;
  body?: unknown;
}

interface WireCall<R = unknown> {
  request: WireRequest;
  response: { status: number; content_type: string; body: R };
}

interface Wire {
  params: Record<string, string>;
  page_size: number;
  sort: string;
  list: WireCall<ListResult<WireItem>>[];
  exchanges: Record<string, WireCall>;
}

const wire = JSON.parse(
  readFileSync(new URL("../../../testdata/sdk_wire.json", import.meta.url), "utf8"),
) as Wire;

function exchange<R>(name: string): WireCall<R> {
  const call = wire.exchanges[name];
  if (!call) throw new Error(`sdk_wire.json records no exchange ${name}`);
  return call as WireCall<R>;
}

function page(i: number): ListResult<WireItem> {
  const call = wire.list[i];
  if (!call) throw new Error(`sdk_wire.json records no list page ${String(i)}`);
  return call.response.body;
}

const schema = exchange<ResourceSchema>("schema").response.body;
const related = Object.fromEntries(
  exchange<{ schemas: ResourceSchema[] }>("schemas").response.body.schemas.map((s) => [s.resource, s]),
) as Record<string, ResourceSchema>;

interface Call {
  method: string;
  url: URL;
  headers: Headers;
  body: unknown;
}

// fakeServer answers from handler and records every request.
function fakeServer(handler: (call: Call) => Response) {
  const calls: Call[] = [];
  const fetchFn: typeof globalThis.fetch = (input, init) => {
    const call: Call = {
      method: init?.method ?? "GET",
      url: new URL(input instanceof Request ? input.url : String(input)),
      headers: new Headers(init?.headers),
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    };
    calls.push(call);
    return Promise.resolve(handler(call));
  };
  return { calls, fetch: fetchFn };
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

interface Held {
  signal: AbortSignal;
  release: () => void;
}

// heldServer answers each request with the schema only when released, and
// rejects one whose signal has aborted, or aborts first, with the signal's
// reason, as fetch does.
function heldServer() {
  const requests: Held[] = [];
  const fetchFn: typeof globalThis.fetch = (_input, init) => {
    const signal = init?.signal;
    if (!signal) throw new Error("request sent without a signal");
    return new Promise<Response>((resolve, reject) => {
      requests.push({ signal, release: () => { resolve(json(schema)); } });
      if (signal.aborted) {
        reject(signal.reason as Error);
        return;
      }
      signal.addEventListener("abort", () => {
        reject(signal.reason as Error);
      }, { once: true });
    });
  };
  return { requests, fetch: fetchFn };
}

// received is the request call is, in the recording's shape.
function received(call: Call): WireRequest {
  const query: Record<string, string[]> = {};
  for (const [name, value] of call.url.searchParams) (query[name] ??= []).push(value);
  const req: WireRequest = { method: call.method, path: call.url.pathname };
  if (Object.keys(query).length > 0) req.query = query;
  const contentType = call.headers.get("Content-Type");
  if (contentType !== null) req.content_type = contentType;
  if (call.body !== undefined) req.body = call.body;
  return req;
}

// replay answers a request equal to one of calls' with its recorded answer,
// and fails any other.
function replay(...calls: WireCall[]) {
  return fakeServer((call) => {
    const req = received(call);
    const recorded = calls.find((c) => isDeepStrictEqual(c.request, req));
    if (!recorded) {
      throw new Error(`unrecorded request ${JSON.stringify(req)}; recorded ${JSON.stringify(calls.map((c) => c.request))}`);
    }
    const { status, content_type, body } = recorded.response;
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": content_type } });
  });
}

function client(fetchFn: typeof globalThis.fetch, validateBeforeSend = false) {
  return createClient({ baseURL: "https://api.test", fetch: fetchFn, validateBeforeSend });
}

// The options the recorded keyset walk was requested with.
const walk = { sort: wire.sort, pageSize: wire.page_size, include: ["tags"], facets: ["name" as const] };

describe("listAll", () => {
  it("sends the recorded requests through the Go-served pages", async () => {
    const server = replay(...wire.list);
    const items = client(server.fetch).resource<WireItem>("wire_items");

    const names: string[] = [];
    for await (const p of items.listAll(walk)) {
      names.push(...p.items.map((i) => i.name));
    }

    expect(names).toEqual(["alpha", "bravo", "charlie", "delta", "echo"]);
    expect(server.calls).toHaveLength(wire.list.length);
  });

  it("surfaces include projections, their truncation and facet counts", async () => {
    const items = client(replay(...wire.list).fetch).resource<WireItem>("wire_items");
    const first = await items.list(walk);
    expect(first).toEqual(page(0));
    expect(first._relations?.["tags"]?.["b"]).toHaveLength(100);
    expect(first._relations?.["tags"]?.["d"]).toEqual(["t001", "t002"]);
    expect(first._truncated?.["tags"]).toEqual(["b"]);
    expect(first.facets?.["name"]?.map((b) => b.key)).toEqual(["alpha", "bravo", "charlie", "delta", "echo"]);
  });

  it("fetches the page before one by its start_cursor", async () => {
    const items = client(replay(exchange("list_backward")).fetch).resource<WireItem>("wire_items");
    const before = await items.list({ ...walk, cursor: page(1).page_info.start_cursor ?? "" });
    expect(before.items).toEqual(page(0).items);
    expect(before.page_info).toMatchObject({ has_next_page: true, has_prev_page: false });
  });
});

describe("list", () => {
  const filtered = { filter: { name: { $ne: "echo" } }, sort: [{ field: "name" as const, order: "desc" as const }], pageSize: 2, includeTotal: true };

  it("reads total_count over GET", async () => {
    const items = client(replay(exchange("list_total")).fetch).resource<WireItem>("wire_items");
    const res = await items.list(filtered);
    expect(res).toEqual(exchange("list_total").response.body);
    expect(res.page_info.total_count).toBe(4);
  });

  it("reads a list that matches nothing as an empty items array", async () => {
    const recorded = exchange<ListResult<WireItem>>("list_empty");
    const items = client(replay(recorded).fetch).resource<WireItem>("wire_items");
    const res = await items.list({ filter: { id: { $eq: "zz" } }, includeTotal: true, include: ["tags"], facets: ["name"] });
    expect(res).toEqual(recorded.response.body);
    expect(res.items.map((i) => i.name)).toEqual([]);
    expect(res.page_info).toEqual({ has_next_page: false, has_prev_page: false, total_count: 0 });
  });

  it("reads a list under a field mask as the members it names", async () => {
    const recorded = exchange<ListResult<WireItem>>("list_masked");
    const items = client(replay(recorded).fetch).resource<WireItem>("wire_items");
    const res = await items.list({ ...filtered, fields: ["name"] });
    expect(res).toEqual(recorded.response.body);
    expect(res.items.map((i) => Object.keys(i))).toEqual(res.items.map(() => ["name"]));
    expect(res.page_info).toEqual(exchange<ListResult<WireItem>>("list_total").response.body.page_info);
  });

  it("sends the recorded list body over POST, and reads the page GET reads", async () => {
    const items = client(replay(exchange("list_post")).fetch).resource<WireItem>("wire_items");
    const res = await items.list(filtered, { method: "POST" });
    expect(res).toEqual(exchange("list_total").response.body);
  });

  it("reads a parse error body as the handler wrote it", async () => {
    const recorded = exchange<Record<string, unknown>>("error").response;
    const items = client(replay(exchange("error")).fetch).resource<Record<string, unknown>>("wire_items");
    const err = await items.list({ filter: { nope: { $eq: 1 } } }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: "E2071", message: recorded.body["message"], body: recorded.body });
  });

  it("sends the server's query parameters", async () => {
    const server = fakeServer(() => json({ items: [], page_info: { has_next_page: false, has_prev_page: false } }));
    await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).list({
      filter: { name: "alpha" },
      sort: [{ field: "name", order: "desc" }],
      cursor: "c1",
      pageSize: 10,
      includeTotal: true,
      includeDeleted: true,
      search: "al",
      include: ["tags"],
      facets: ["name"],
    });
    const params = server.calls[0]?.url.searchParams;
    // Parameter names come from the Go filter.Param* constants.
    const p = (name: string): string => wire.params[name] ?? `missing ${name}`;
    expect(Object.fromEntries(params ?? [])).toEqual({
      [p("ParamFilter")]: JSON.stringify({ name: "alpha" }),
      [p("ParamSort")]: "-name",
      [p("ParamCursor")]: "c1",
      [p("ParamPageSize")]: "10",
      [p("ParamIncludeTotal")]: "true",
      [p("ParamIncludeDeleted")]: "true",
      [p("ParamSearch")]: "al",
      [p("ParamInclude")]: "tags",
      [p("ParamFacets")]: "name",
    });
  });

  it("validates against the Go-emitted schema before sending", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(schema) : json(page(0))));
    const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });

    await expect(items.list({ filter: { name: "zulu" } })).rejects.toMatchObject({ code: "E2087" });
    await expect(items.list({ filter: { labels: { $gt: 1 } } })).rejects.toBeInstanceOf(SchemaValidationError);
    await expect(items.list({ filter: { labels: { env: "prod" } } })).resolves.toBeDefined();
    await expect(items.list({ filter: { tags: { $in: ["t1"] } } as never })).resolves.toBeDefined();
    await expect(items.list({ include: ["tags"] })).resolves.toBeDefined();
    await expect(items.list({ include: ["name"] })).rejects.toMatchObject({ code: "E2083" });
    await expect(items.list({ include: ["owner"] })).rejects.toMatchObject({ code: "E2071" });
    await expect(items.list({ facets: ["name"] })).resolves.toBeDefined();
    await expect(items.list({ facets: ["labels"] })).rejects.toMatchObject({ code: "E2071" });
  });
});

describe("list transport", () => {
  const empty = { items: [], page_info: { has_next_page: false, has_prev_page: false } };
  const p = (name: string): string => wire.params[name] ?? `missing ${name}`;
  const big = { name: { $in: Array.from({ length: 200 }, (_, i) => `name-${String(i).padStart(4, "0")}`) } };
  const withMaxURLLength = (fetchFn: typeof globalThis.fetch, maxURLLength: number) =>
    createClient({ baseURL: "https://api.test", fetch: fetchFn, maxURLLength });

  // A server without a POST list route answers 405, or hands the body to its
  // create handler, so the body transport is the caller's to switch on.
  it("keeps every list a GET, however long, unless maxURLLength is configured", async () => {
    const server = fakeServer(() => json(empty));
    const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
    const huge = { name: { $in: Array.from({ length: 1000 }, (_, i) => `name-${String(i).padStart(4, "0")}`) } };
    await items.list({ filter: huge });
    for await (const page of items.listAll({ filter: huge })) {
      expect(page.items).toEqual([]);
    }
    expect(server.calls.map((c) => c.method)).toEqual(["GET", "GET"]);
    const first = server.calls[0];
    expect(first?.url.searchParams.get(p("ParamFilter"))).toBe(JSON.stringify(huge));
    expect(first?.url.href.length).toBeGreaterThan(16 << 10);
  });

  it("sends a list past a configured maxURLLength as a POST body with the same members", async () => {
    const server = fakeServer(() => json(empty));
    await withMaxURLLength(server.fetch, 2000).resource<WireItem>("items", { schema: "wire_items" }).list({
      filter: big,
      sort: "-name",
      cursor: "c1",
      pageSize: 10,
      includeTotal: true,
      includeDeleted: true,
      search: "al",
      include: ["tags"],
      facets: ["name"],
    });
    const call = server.calls[0];
    expect(call?.method).toBe("POST");
    expect(call?.url.pathname).toBe("/items");
    expect(call?.url.search).toBe("");
    // Member names come from the Go filter.Param* constants.
    expect(call?.body).toEqual({
      [p("ParamFilter")]: big,
      [p("ParamSort")]: "-name",
      [p("ParamCursor")]: "c1",
      [p("ParamPageSize")]: 10,
      [p("ParamIncludeTotal")]: true,
      [p("ParamIncludeDeleted")]: true,
      [p("ParamSearch")]: "al",
      [p("ParamInclude")]: ["tags"],
      [p("ParamFacets")]: ["name"],
    });
  });

  it("keeps a list a GET up to a configured maxURLLength and a POST past it", async () => {
    const server = fakeServer(() => json(empty));
    const opts = { filter: { name: "alpha" } };
    await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).list(opts);
    const length = server.calls[0]?.url.href.length ?? 0;
    expect(length).toBeGreaterThan(0);
    await withMaxURLLength(server.fetch, length).resource<WireItem>("items", { schema: "wire_items" }).list(opts);
    await withMaxURLLength(server.fetch, length - 1).resource<WireItem>("items", { schema: "wire_items" }).list(opts);
    expect(server.calls.map((c) => c.method)).toEqual(["GET", "GET", "POST"]);
  });

  it("lets method force either transport", async () => {
    const server = fakeServer(() => json(empty));
    await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).list({ filter: { name: "alpha" } }, { method: "POST" });
    await withMaxURLLength(server.fetch, 2000).resource<WireItem>("items", { schema: "wire_items" }).list({ filter: big }, { method: "GET" });
    expect(server.calls.map((c) => c.method)).toEqual(["POST", "GET"]);
    expect(server.calls[0]?.body).toEqual({ [p("ParamFilter")]: { name: "alpha" } });
    expect(server.calls[1]?.url.searchParams.get(p("ParamFilter"))).toBe(JSON.stringify(big));
  });

  it("refuses a maxURLLength that is not a positive integer", () => {
    for (const maxURLLength of [0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(() => createClient({ baseURL: "https://api.test", maxURLLength }), String(maxURLLength)).toThrow(RangeError);
    }
  });

  it("follows cursors through POST pages", async () => {
    const server = fakeServer(({ body }) => {
      const cursor = (body as Record<string, unknown> | undefined)?.[p("ParamCursor")] ?? "";
      const recorded = wire.list.find((c) => (c.request.query?.[p("ParamCursor")]?.[0] ?? "") === cursor);
      return recorded ? json(recorded.response.body) : json({ code: "E2082", message: "invalid cursor" }, 422);
    });
    const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
    const names: string[] = [];
    for await (const page of items.listAll({ sort: wire.sort, pageSize: wire.page_size }, { method: "POST" })) {
      names.push(...page.items.map((i) => i.name));
    }
    expect(names).toEqual(["alpha", "bravo", "charlie", "delta", "echo"]);
    expect(server.calls.every((c) => c.method === "POST")).toBe(true);
  });

  // The body transport is list's alone: the server contract has no body form
  // for a facet request or a patch's ids.
  it("sends facets and patchMany in the URL whatever maxURLLength", async () => {
    const server = fakeServer(() => json({ facets: {}, updated: [] }));
    const items = withMaxURLLength(server.fetch, 30).resource<WireItem>("items", { schema: "wire_items" });
    await items.facets(["name"], { filter: big });
    await items.patchMany(["a", "b"], { name: "bravo" });
    expect(server.calls.map((c) => c.method)).toEqual(["OPTIONS", "PATCH"]);
    expect(server.calls[0]?.url.searchParams.get(p("ParamFilter"))).toBe(JSON.stringify(big));
    expect(server.calls[1]?.url.searchParams.getAll("id[]")).toEqual(["a", "b"]);
  });

  // The Go side (TestFilterConformance_ThroughBothListTransports) holds that
  // the server reads ?filter= and a body's filter member alike; this holds
  // that the client sends each corpus filter unchanged on both.
  it("sends every filter conformance input unchanged on both transports", async () => {
    const corpus = JSON.parse(
      readFileSync(new URL("../../../testdata/filter_conformance.json", import.meta.url), "utf8"),
    ) as { cases: { name: string; input: Record<string, unknown>; tsApplicable?: boolean }[] };
    const cases = corpus.cases.filter((c) => c.tsApplicable !== false);
    expect(cases.length).toBeGreaterThan(0);

    const server = fakeServer(() => json(empty));
    const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
    for (const c of cases) {
      await items.list({ filter: c.input }, { method: "GET" });
      await items.list({ filter: c.input }, { method: "POST" });
    }
    cases.forEach((c, i) => {
      const get = server.calls[2 * i];
      const post = server.calls[2 * i + 1];
      // An empty filter filters nothing, so neither transport sends it.
      const sent = Object.keys(c.input).length > 0;
      expect(get?.method, c.name).toBe("GET");
      expect(JSON.parse(get?.url.searchParams.get(p("ParamFilter")) ?? "null"), c.name).toEqual(sent ? c.input : null);
      expect(post?.method, c.name).toBe("POST");
      expect(post?.body, c.name).toEqual(sent ? { [p("ParamFilter")]: c.input } : {});
    });
  });
});

describe("relation filters", () => {
  it("validates a nested filter against the target's Go-emitted schema", async () => {
    const server = fakeServer(({ url }) => {
      if (url.pathname.endsWith("/wire_parts/schema")) return json(related["wire_parts"]);
      if (url.pathname.endsWith("/schema")) return json(schema);
      return json(page(0));
    });
    const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });

    await expect(items.list({ filter: { parts: { $some: { state: "worn" } } } as never })).resolves.toBeDefined();
    await expect(items.list({ filter: { parts: { $some: { colour: "red" } } } as never })).rejects.toMatchObject({ code: "E2071" });
    await expect(items.list({ filter: { parts: { $some: { state: "new" } } } as never })).rejects.toMatchObject({ code: "E2087" });
    await expect(items.list({ filter: { parts: { $is: {} } } as never })).rejects.toMatchObject({ code: "E2072" });
    await expect(items.list({ filter: { parts: "p1" } as never })).resolves.toBeDefined();
    const schemaCalls = server.calls.filter((c) => c.url.pathname.endsWith("/wire_parts/schema"));
    expect(schemaCalls).toHaveLength(1);
  });
});

describe("errors", () => {
  function problemResponse(doc: unknown, status: number): Response {
    return new Response(JSON.stringify(doc), { status, headers: { "Content-Type": "application/problem+json; charset=utf-8" } });
  }

  // The RFC 9457 §3 validation example, with code extensions.
  const validation = {
    type: "https://example.net/validation-error",
    title: "Your request is not valid.",
    status: 422,
    detail: "2 members are invalid",
    code: "E2077",
    errors: [
      { detail: "must be a positive integer", pointer: "#/age" },
      { detail: "must be 'green', 'red' or 'blue'", pointer: "#/profile/color", code: "E2087" },
    ],
  };
  const parseBody = exchange<{ code: string; message: string }>("error").response.body;
  const validationProblem: Problem = {
    type: validation.type,
    title: validation.title,
    status: 422,
    detail: validation.detail,
    extensions: { code: "E2077", errors: validation.errors },
  };
  const validationErrors: FieldError[] = [
    { detail: "must be a positive integer", pointer: "#/age" },
    { detail: "must be 'green', 'red' or 'blue'", pointer: "#/profile/color", code: "E2087" },
  ];

  const cases: { name: string; response: () => Response; want: Record<string, unknown> }[] = [
    {
      name: "a parse error body",
      response: () => json(parseBody, 422),
      want: { status: 422, code: "E2071", message: parseBody.message, body: parseBody, problem: undefined, errors: [] },
    },
    {
      name: "an RFC 9457 problem with field errors",
      response: () => problemResponse(validation, 422),
      want: {
        status: 422,
        code: "E2077",
        message: "2 members are invalid",
        body: { code: "E2077", message: "2 members are invalid" },
        problem: validationProblem,
        errors: validationErrors,
      },
    },
    {
      name: "a problem without a code extension, by its type and title",
      response: () => problemResponse({ type: "https://example.com/probs/out-of-credit", title: "You do not have enough credit." }, 403),
      want: { status: 403, code: "https://example.com/probs/out-of-credit", message: "You do not have enough credit.", errors: [] },
    },
    {
      name: "an about:blank problem recognized by its title and status",
      response: () => json({ title: "Not Found", status: 404 }, 404),
      want: { status: 404, code: "", message: "Not Found", problem: { type: "about:blank", title: "Not Found", status: 404, extensions: {} } },
    },
    {
      name: "a problem recognized by its absolute type",
      response: () => json({ type: "https://example.com/probs/out-of-credit", detail: "Your balance is 30." }, 403),
      want: { status: 403, code: "https://example.com/probs/out-of-credit", message: "Your balance is 30." },
    },
    {
      name: "JSON whose type is a bare word, not as a problem",
      response: () => json({ type: "error", error: { message: "bad" } }, 400),
      want: { status: 400, code: "", message: "HTTP 400", problem: undefined },
    },
    {
      name: "JSON with only a status, not as a problem",
      response: () => json({ timestamp: "2026-11-20T00:00:00Z", status: 500, error: "Internal Server Error", path: "/items" }, 500),
      want: { status: 500, code: "", message: "HTTP 500", problem: undefined },
    },
    {
      name: "a problem with an empty type as about:blank",
      response: () => problemResponse({ type: "", title: "Bad Request" }, 400),
      want: { status: 400, code: "", message: "Bad Request", problem: { type: "about:blank", title: "Bad Request" } },
    },
    {
      name: "a parse error body sent as a problem",
      response: () => problemResponse(parseBody, 422),
      want: { code: "E2071", message: parseBody.message, body: parseBody, problem: { type: "about:blank", extensions: parseBody } },
    },
    {
      name: "a non-JSON body",
      response: () => new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" }),
      want: { status: 502, code: "", message: "Bad Gateway", problem: undefined, errors: [] },
    },
    {
      name: "an empty body",
      response: () => new Response(null, { status: 503, statusText: "Service Unavailable" }),
      want: { status: 503, code: "", message: "Service Unavailable", problem: undefined, errors: [] },
    },
    {
      name: "an empty body without a status text",
      response: () => new Response(null, { status: 500 }),
      want: { status: 500, code: "", message: "HTTP 500", problem: undefined },
    },
    {
      name: "JSON in neither shape",
      response: () => json({ error: "boom" }, 500),
      want: { status: 500, code: "", message: "HTTP 500", problem: undefined },
    },
    {
      name: "a problem document that is not an object",
      response: () => problemResponse(["not", "a", "problem"], 400),
      want: { status: 400, code: "", message: "HTTP 400", problem: undefined },
    },
  ];

  it.each(cases)("reads $name", async ({ response, want }) => {
    const err = await client(fakeServer(response).fetch).resource<WireItem>("items", { schema: "wire_items" }).list().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject(want);
  });

  it("asks for problem documents as well as JSON", async () => {
    const server = fakeServer(() => json(page(0)));
    await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).list();
    expect(server.calls[0]?.headers.get("Accept")).toBe("application/json, application/problem+json");
  });
});

describe("schema", () => {
  it("retries after a failed fetch and shares an in-flight one", async () => {
    let fail = true;
    const server = fakeServer(() => {
      if (fail) {
        fail = false;
        return json({ code: "E5001", message: "unavailable" }, 503);
      }
      return json(schema);
    });
    const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });

    await expect(items.schema()).rejects.toMatchObject({ status: 503 });
    const [a, b] = await Promise.all([items.schema(), items.schema()]);
    expect(a).toEqual(schema);
    expect(b).toBe(a);
    expect(server.calls).toHaveLength(2);
    await items.schema();
    expect(server.calls).toHaveLength(2);
  });

  describe("a shared fetch", () => {
    function request(server: { requests: Held[] }, i: number): Held {
      const held = server.requests[i];
      if (!held) throw new Error(`no request ${String(i)}`);
      return held;
    }

    it("survives the first caller's abort for a second caller", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
      const first = new AbortController();

      const a = items.schema({ signal: first.signal });
      const b = items.schema();
      first.abort();

      await expect(a).rejects.toMatchObject({ name: "AbortError" });
      expect(server.requests).toHaveLength(1);
      expect(request(server, 0).signal.aborted).toBe(false);
      request(server, 0).release();
      await expect(b).resolves.toEqual(schema);
    });

    it("rejects only a second caller that aborts", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
      const second = new AbortController();

      const a = items.schema();
      const b = items.schema({ signal: second.signal });
      second.abort();

      await expect(b).rejects.toMatchObject({ name: "AbortError" });
      request(server, 0).release();
      await expect(a).resolves.toEqual(schema);
      expect(server.requests).toHaveLength(1);
    });

    it("is cancelled when every caller aborts, and the next call refetches", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
      const one = new AbortController();
      const two = new AbortController();

      const a = items.schema({ signal: one.signal });
      const b = items.schema({ signal: two.signal });
      one.abort();
      await expect(a).rejects.toMatchObject({ name: "AbortError" });
      expect(request(server, 0).signal.aborted).toBe(false);
      two.abort(new Error("navigated away"));
      await expect(b).rejects.toThrow("navigated away");
      expect(request(server, 0).signal.aborted).toBe(true);

      const c = items.schema();
      expect(server.requests).toHaveLength(2);
      request(server, 1).release();
      await expect(c).resolves.toEqual(schema);
      await items.schema();
      expect(server.requests).toHaveLength(2);
    });

    it("sends nothing for a caller that has already aborted", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items", { schema: "wire_items" });
      await expect(items.schema({ signal: AbortSignal.abort() })).rejects.toMatchObject({ name: "AbortError" });
      expect(server.requests).toHaveLength(0);
    });

    it("ends a validating list's wait without failing the other caller's", async () => {
      const server = heldServer();
      const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
      const first = new AbortController();

      const listing = items.list({ sort: "name" }, { signal: first.signal });
      const validator = items.validator();
      first.abort();

      await expect(listing).rejects.toMatchObject({ name: "AbortError" });
      request(server, 0).release();
      expect((await validator).filterableFields()).toContain("name");
      expect(server.requests).toHaveLength(1);
    });
  });

  it("reads the version the Go package serves", () => {
    expect(schema.version).toBe(SCHEMA_VERSION);
  });

  it("sends the recorded schema requests and reads their answers", async () => {
    const server = replay(exchange("schema"), exchange("schemas"), exchange("schema_not_found"));
    const c = client(server.fetch);
    await expect(c.resource<WireItem>("wire_items").schema()).resolves.toEqual(schema);
    await expect(c.schemas()).resolves.toEqual(Object.values(related));
    const missing = await c.resource<WireItem>("gadgets").schema().catch((e: unknown) => e);
    expect(missing).toBeInstanceOf(ApiError);
    expect(missing).toMatchObject({ status: 404, code: "E2078", body: exchange("schema_not_found").response.body });
  });

  it("refuses a document in a version it does not read, without caching it", async () => {
    const future = { ...schema, version: SCHEMA_VERSION + 1 };
    const server = fakeServer(() => json(future));
    const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });

    const err = await items.schema().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(SchemaVersionError);
    expect(err).toMatchObject({ resource: "wire_items", version: SCHEMA_VERSION + 1, supported: SCHEMA_VERSION });
    await expect(items.list({ sort: "name" })).rejects.toBeInstanceOf(SchemaVersionError);
    expect(server.calls).toHaveLength(2);
  });

  it("refuses a listing that holds a document in another version", async () => {
    const server = fakeServer(() => json({ schemas: [schema, { ...schema, resource: "other", version: 0 }] }));
    await expect(client(server.fetch).schemas()).rejects.toBeInstanceOf(SchemaVersionError);
  });
});

describe("patch", () => {
  const updated = exchange<PatchResult<WireItem>>("patch").response.body;

  it("sends the recorded merge patch and reads the rows it updated", async () => {
    const items = client(replay(exchange("patch")).fetch).resource<WireItem>("wire_items");
    await expect(items.patch("b", { labels: { env: "prod" } })).resolves.toEqual(updated);
  });

  it("sends the recorded batch patch and reads the keys it could not update", async () => {
    const recorded = exchange<PatchResult<WireItem>>("patch_many").response.body;
    const items = client(replay(exchange("patch_many")).fetch).resource<WireItem>("wire_items");
    const res = await items.patchMany(["d", "zz"], { labels: { env: "test" } });
    expect(res).toEqual(recorded);
    expect(res.updated.map((i) => i.id)).toEqual(["d"]);
    expect(res.failed?.map((f) => f.id)).toEqual(["zz"]);
  });

  it("sends a merge patch to the entity", async () => {
    const server = fakeServer(() => json(updated));
    const res = await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).patch("a/b", { name: "bravo", labels: null });
    expect(server.calls[0]).toMatchObject({ method: "PATCH", body: { name: "bravo", labels: null } });
    expect(server.calls[0]?.url.pathname).toBe("/items/a%2Fb");
    expect(res.updated).toHaveLength(1);
  });

  it("patches several entities through id[] parameters", async () => {
    const server = fakeServer(() => json(updated));
    await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).patchMany(["a", "b"], { name: "bravo" });
    expect(server.calls[0]?.method).toBe("PATCH");
    expect(server.calls[0]?.url.pathname).toBe("/items");
    expect(server.calls[0]?.url.searchParams.getAll("id[]")).toEqual(["a", "b"]);
  });

  it("sends the recorded patch of a row keyed by two columns, a path segment per column", async () => {
    const recorded = exchange<PatchResult<WireStock>>("patch_composite_key");
    expect(recorded.request.path).toBe("/wire_stock/w1/a%2Fb");
    const stock = client(replay(recorded).fetch).resource<WireStock, [string, string]>("wire_stock");
    await expect(stock.patch(["w1", "a/b"], { qty: 3 })).resolves.toEqual(recorded.response.body);
  });

  it("writes number, bigint and boolean key values as their text", async () => {
    const server = fakeServer(() => json(updated));
    await client(server.fetch).resource<WireStock, [string, number]>("bins").patch(["w 1", 7], { qty: 1 });
    await client(server.fetch).resource<WireItem>("items").patch(12345678901234567890n, { name: "alpha" });
    await client(server.fetch).resource<WireItem>("flags").patch(true, { name: "alpha" });
    await client(server.fetch).resource<WireItem>("items").patchMany([7, 8n], { name: "alpha" });
    expect(server.calls.map((c) => c.url.pathname)).toEqual(["/bins/w%201/7", "/items/12345678901234567890", "/flags/true", "/items"]);
    expect(server.calls[3]?.url.searchParams.getAll("id[]")).toEqual(["7", "8"]);
  });

  it("refuses a key of another arity than the schema's key before sending", async () => {
    const server = fakeServer(({ url }) => {
      if (url.pathname.endsWith("/wire_stock/schema")) return json(related["wire_stock"]);
      if (url.pathname.endsWith("/schema")) return json(schema);
      return json({ updated: [] });
    });
    const stock = client(server.fetch, true).resource<WireStock, [string, string]>("wire_stock");
    // @ts-expect-error a key of two columns takes two values
    await expect(stock.patch(["w1"], { qty: 3 })).rejects.toThrow("the key of wire_stock is (warehouse_id, sku), a value per column; got 1");
    // @ts-expect-error a key of two columns takes no third value
    await expect(stock.patch(["w1", "a", "x"], { qty: 3 })).rejects.toBeInstanceOf(RangeError);
    // @ts-expect-error a key of two columns is a tuple, never one value
    await expect(stock.patch("w1", { qty: 3 })).rejects.toBeInstanceOf(RangeError);
    const items = client(server.fetch, true).resource<WireItem>("wire_items");
    // @ts-expect-error a key of one column is one value
    await expect(items.patch(["a", "b"], { name: "alpha" })).rejects.toThrow("the key of wire_items is (id), a value per column; got 2");
    await expect(stock.patch(["w1", "a/b"], { qty: 3 })).resolves.toBeDefined();
    expect(server.calls.filter((c) => c.method === "PATCH").map((c) => c.url.pathname)).toEqual(["/wire_stock/w1/a%2Fb"]);
  });

  // id[] holds one value per row, so a key of several columns has no form in
  // it; such rows are patched one by one.
  it("refuses patchMany on a key of several columns before sending", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(related["wire_stock"]) : json({ updated: [] })));
    const typed = client(server.fetch).resource<WireStock, [string, string]>("wire_stock");
    // @ts-expect-error patchMany takes no tuple key
    await expect(typed.patchMany([["w1", "a"]], { qty: 3 })).rejects.toBeInstanceOf(RangeError);
    const untyped = client(server.fetch, true).resource<WireStock>("wire_stock");
    await expect(untyped.patchMany(["w1"], { qty: 3 })).rejects.toThrow("patchMany cannot address wire_stock: its key is (warehouse_id, sku)");
    expect(server.calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
  });

  it("validates null against the Go-emitted schema", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(schema) : json(updated)));
    const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    // name is a non-nullable string: null is not one of its values, as the server answers.
    await expect(items.patch("a", { name: null })).rejects.toMatchObject({ code: "E2077" });
    await expect(items.patch("a", { id: "x" })).rejects.toMatchObject({ code: "E2076" });
    await expect(items.patch("a", { labels: null })).resolves.toBeDefined();
  });

  it("looks up field names as escaped JSON Pointer tokens", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(schema) : json(updated)));
    const items = client(server.fetch, true).resource<Record<string, unknown>>("items", { schema: "wire_items" });
    // The Go schema holds "/unit~1si~0v" for the field named "unit/si~v".
    await expect(items.patch("a", { "unit/si~v": "kg" })).resolves.toBeDefined();
    await expect(items.patch("a", { "unit~1si~0v": "kg" })).rejects.toMatchObject({ code: "E2076" });
  });
});

describe("facets", () => {
  it("sends the recorded OPTIONS ?facets= and reads its counts", async () => {
    const recorded = exchange<{ facets: FacetResult }>("facets").response.body;
    const items = client(replay(exchange("facets")).fetch).resource<WireItem>("wire_items");
    const res = await items.facets(["name"], { filter: { name: { $ne: "echo" } } });
    expect(res).toEqual(recorded.facets);
    expect(res["name"]?.map((b) => b.key)).toEqual(["alpha", "bravo", "charlie", "delta"]);
  });

  it("sends the search the counts describe", async () => {
    const server = fakeServer(() => json({ facets: {} }));
    await client(server.fetch).resource<WireItem>("items", { schema: "wire_items" }).facets(["name"], { search: "cordless drill" });
    expect(server.calls[0]?.url.searchParams.get(wire.params["ParamSearch"] ?? "")).toBe("cordless drill");
  });
});

// outcome is what a call settled with: its value, or its error's class and
// fields, so two calls' outcomes are equal only when they decoded an answer
// alike.
async function outcome(call: Promise<unknown>): Promise<unknown> {
  try {
    return { value: await call };
  } catch (error) {
    if (!(error instanceof Error)) throw error;
    return { error: error.constructor, message: error.message, fields: Object.fromEntries(Object.entries(error)) };
  }
}

// The routes a generated SDK binds beside parse's: create, get, delete and
// the rest go through request, on the client or on a handle.
describe("request", () => {
  interface NewItem {
    name: string;
  }

  const sends: { name: string; send: (c: Client) => Promise<unknown>; want: { method: string; url: string; body?: unknown } }[] = [
    {
      name: "a body and a query to a path below baseURL",
      send: (c) => c.request<WireItem, NewItem>("POST", "/items/", { body: { name: "alpha" }, query: { dry_run: "true" } }),
      want: { method: "POST", url: "https://api.test/v1/items?dry_run=true", body: { name: "alpha" } },
    },
    {
      name: "the row of a one-column key, its value escaped",
      send: (c) => c.resource<WireItem>("items").request("GET", { key: "a/b" }),
      want: { method: "GET", url: "https://api.test/v1/items/a%2Fb" },
    },
    {
      name: "the row of a two-column key, a segment per column",
      send: (c) => c.resource<WireStock, [string, string]>("stock").request("PUT", { key: ["w 1", "a/b"], body: { warehouse_id: "w 1", sku: "a/b", qty: 3 } }),
      want: { method: "PUT", url: "https://api.test/v1/stock/w%201/a%2Fb", body: { warehouse_id: "w 1", sku: "a/b", qty: 3 } },
    },
    {
      name: "a path below a row",
      send: (c) => c.resource<WireItem>("items").request("POST", { key: 12345678901234567890n, path: "/archive/" }),
      want: { method: "POST", url: "https://api.test/v1/items/12345678901234567890/archive" },
    },
    {
      name: "a path below the collection, with a repeated parameter",
      send: (c) => c.resource<WireItem>("shop/items").request("GET", { path: "search", query: new URLSearchParams([["tag", "a"], ["tag", "b c"]]) }),
      want: { method: "GET", url: "https://api.test/v1/shop/items/search?tag=a&tag=b+c" },
    },
    {
      name: "the collection, and a JSON null body as a body",
      send: (c) => c.resource<WireItem>("items").request("POST", { body: null }),
      want: { method: "POST", url: "https://api.test/v1/items", body: null },
    },
  ];

  it.each(sends)("sends $name, with the client's headers and the call's", async ({ send, want }) => {
    const server = fakeServer(() => json({ id: "a" }));
    const c = createClient({ baseURL: "https://api.test/v1/", fetch: server.fetch, headers: { Authorization: "Bearer t" } });
    await send(c);
    const call = server.calls[0];
    expect({ method: call?.method, url: call?.url.href, body: call?.body }).toEqual({ body: undefined, ...want });
    expect(call?.headers.get("Accept")).toBe("application/json, application/problem+json");
    expect(call?.headers.get("Authorization")).toBe("Bearer t");
    expect(call?.headers.get("Content-Type")).toBe("body" in want ? "application/json" : null);
    expect(server.calls).toHaveLength(1);
  });

  it("sends a call's headers over the client's", async () => {
    const server = fakeServer(() => json({}));
    const c = createClient({ baseURL: "https://api.test", fetch: server.fetch, headers: { Authorization: "Bearer t" } });
    await c.request("POST", "items", { body: {} }, { headers: { authorization: "Bearer u", "Idempotency-Key": "k1" } });
    expect(server.calls[0]?.headers.get("Authorization")).toBe("Bearer u");
    expect(server.calls[0]?.headers.get("Idempotency-Key")).toBe("k1");
  });

  // parse serves no create, get or delete, so the corpus has no exchange of
  // one; replaying parse's recorded exchanges through request holds its path,
  // key segments, body and answers to the requests the Go client builds and
  // the answers the Go handlers gave.
  it("sends the recorded requests and reads the recorded answers as patch, list and schema read them", async () => {
    const recordedQuery = (call: WireCall) =>
      new URLSearchParams(Object.entries(call.request.query ?? {}).flatMap(([name, values]) => values.map((v) => [name, v])));
    const patched = exchange<PatchResult<WireStock>>("patch_composite_key");
    const listed = exchange<ListResult<WireItem>>("list_total");
    const refused = exchange<Record<string, unknown>>("error");
    const missing = exchange<Record<string, unknown>>("schema_not_found");
    const c = client(replay(patched, listed, refused, missing).fetch);

    const stock = c.resource<WireStock, [string, string]>("wire_stock");
    await expect(stock.request<PatchResult<WireStock>, PatchInput<WireStock>>("PATCH", { key: ["w1", "a/b"], body: { qty: 3 } }))
      .resolves.toEqual(patched.response.body);
    await expect(c.request<ListResult<WireItem>>("GET", "wire_items", { query: recordedQuery(listed) })).resolves.toEqual(listed.response.body);
    await expect(c.request("GET", "wire_items", { query: recordedQuery(refused) })).rejects.toMatchObject({
      status: 422, code: "E2071", body: refused.response.body, problem: undefined,
    });
    const err = await c.resource<WireItem>("gadgets").request("GET", { path: "schema" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 404, code: "E2078", body: missing.response.body });
  });

  const updated = exchange<PatchResult<WireItem>>("patch").response.body;
  const answers: { name: string; response: () => Response }[] = [
    { name: "a JSON 200", response: () => json(updated) },
    { name: "a 204", response: () => new Response(null, { status: 204 }) },
    { name: "an empty 200", response: () => new Response("", { status: 200 }) },
    { name: "an HTML 200", response: () => new Response("<html>ok</html>", { status: 200, headers: { "Content-Type": "text/html" } }) },
    { name: "a parse error body", response: () => json(exchange("error").response.body, 422) },
    {
      name: "a problem document with field errors",
      response: () => new Response(JSON.stringify({ type: "https://example.net/validation-error", title: "Invalid", status: 422, code: "E2077", errors: [{ pointer: "#/name", detail: "too long" }] }), {
        status: 422, headers: { "Content-Type": "application/problem+json" },
      }),
    },
    { name: "an HTML 502", response: () => new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" }) },
  ];

  describe.each([false, true])("with validateBeforeSend %s", (validate) => {
    const answering = (response: () => Response) => fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(schema) : response()));

    it.each(answers)("reads $name as patch reads it, sending nothing else", async ({ response }) => {
      const patchServer = answering(response);
      const viaPatch = await outcome(client(patchServer.fetch, validate).resource<WireItem>("items", { schema: "wire_items" }).patch("a", { name: "bravo" }));
      const requestServer = answering(response);
      const items = client(requestServer.fetch, validate).resource<WireItem>("items", { schema: "wire_items" });
      const viaRequest = await outcome(items.request<PatchResult<WireItem>>("PATCH", { key: "a", body: { name: "bravo" } }));

      expect(viaRequest).toEqual(viaPatch);
      const sent = (calls: Call[]) => calls.filter((c) => c.method === "PATCH").map((c) => ({ url: c.url.href, body: c.body }));
      expect(sent(requestServer.calls)).toEqual(sent(patchServer.calls));
      expect(requestServer.calls).toHaveLength(1);
    });

    it.each(answers)("resolves undefined on a 2xx and rejects a non-2xx as patch does under response none: $name", async ({ response }) => {
      const patchOutcome = await outcome(client(answering(response).fetch, validate).resource<WireItem>("items", { schema: "wire_items" }).patch("a", { name: "bravo" }));
      let answered: Response | undefined;
      const server = fakeServer(() => {
        answered = response();
        return answered;
      });
      const got = await outcome(client(server.fetch, validate).resource<WireItem>("items").request<undefined>("DELETE", { key: "a", response: "none" }));

      if (answered?.ok) {
        expect(got).toEqual({ value: undefined });
        // A body the call does not read is cancelled, which releases the connection.
        expect(answered.body === null || answered.bodyUsed).toBe(true);
      } else {
        expect(got).toEqual(patchOutcome);
      }
      expect(server.calls.map((c) => c.method)).toEqual(["DELETE"]);
    });

    it("refuses a non-finite number in the body before sending anything", async () => {
      const server = fakeServer(() => json({}));
      const c = client(server.fetch, validate);
      const nan = { name: "NonFiniteNumberError", payload: "request body", pointer: "/labels/readings/1", value: Number.NEGATIVE_INFINITY };
      const body = { name: "alpha", labels: { readings: [1, Number.NEGATIVE_INFINITY] } };
      await expect(c.request("POST", "items", { body })).rejects.toMatchObject(nan);
      await expect(c.resource<WireItem>("items", { schema: "wire_items" }).request("PUT", { key: "a", body })).rejects.toMatchObject(nan);
      await expect(c.request("POST", "items", { body: Number.NaN })).rejects.toMatchObject({ payload: "request body", pointer: "" });
      await expect(c.request("POST", "items", { body })).rejects.toBeInstanceOf(NonFiniteNumberError);
      expect(server.calls).toHaveLength(0);
    });
  });

  it("ends on its signal's abort with the signal's reason, in flight or before", async () => {
    const server = heldServer();
    const { request } = client(server.fetch).resource<WireItem>("items");
    const controller = new AbortController();

    const pending = request<undefined>("DELETE", { key: "a", response: "none" }, { signal: controller.signal });
    expect(server.requests).toHaveLength(1);
    controller.abort(new Error("navigated away"));
    await expect(pending).rejects.toThrow("navigated away");
    expect(server.requests[0]?.signal.aborted).toBe(true);

    await expect(request("GET", { key: "a" }, { signal: AbortSignal.abort() })).rejects.toMatchObject({ name: "AbortError" });
  });

  it("refuses a key no path segment carries before sending", async () => {
    const server = fakeServer(() => json({}));
    const items = client(server.fetch).resource<WireItem>("items");
    for (const key of ["", ".", "..", Number.NaN, Number.POSITIVE_INFINITY]) {
      await expect(items.request("GET", { key }), String(key)).rejects.toBeInstanceOf(RangeError);
    }
    await expect(items.request("GET", { key: [] as unknown as string })).rejects.toBeInstanceOf(RangeError);
    await expect(items.request("GET", { key: {} as unknown as string })).rejects.toBeInstanceOf(TypeError);
    await expect(client(server.fetch).resource<WireStock, [string, string]>("stock").request("GET", { key: ["w1", ".."] })).rejects.toBeInstanceOf(RangeError);
    expect(server.calls).toHaveLength(0);
  });

  it("takes the key, body and response types the caller states", async () => {
    const server = fakeServer(() => new Response(null, { status: 204 }));
    const stock = client(server.fetch).resource<WireStock, [string, string]>("stock");
    // @ts-expect-error a key of two columns takes two values
    await stock.request<undefined>("DELETE", { key: ["w1"], response: "none" });
    // @ts-expect-error the body is the type the caller states
    await stock.request<undefined, Pick<WireStock, "qty">>("POST", { body: { qty: "3" }, response: "none" });
    // @ts-expect-error a call that reads no body resolves undefined, which a row is not
    await stock.request<WireStock>("DELETE", { key: ["w1", "a"], response: "none" });
    const gone = stock.request<undefined>("DELETE", { key: ["w1", "a"], response: "none" });
    expectTypeOf(gone).resolves.toBeUndefined();
    await expect(gone).resolves.toBeUndefined();
    const created = stock.request<WireStock, WireStock>("POST", { body: { warehouse_id: "w1", sku: "a", qty: 1 } });
    expectTypeOf(created).resolves.toEqualTypeOf<WireStock>();
    await expect(created).rejects.toBeInstanceOf(ResponseError);
  });
});

// The example server's mux (parse/examples/server/server.go): its route
// patterns exactly, http.ServeMux's 307 for a path holding "//", and 404
// text/plain for anything else, so a URL the client builds wrong fails here
// as it would against the server.
function exampleServer(schemas: Record<string, ResourceSchema>) {
  const page = { items: [], page_info: { has_next_page: false, has_prev_page: false } };
  return fakeServer(({ method, url }) => {
    const path = url.pathname;
    if (path.includes("//")) return new Response(null, { status: 307, headers: { Location: path.replace(/\/{2,}/g, "/") } });
    const schemaRoute = /^\/api\/([^/]+)\/schema$/.exec(path);
    if (method === "GET" && schemaRoute) {
      const doc = schemas[decodeURIComponent(schemaRoute[1] ?? "")];
      return doc ? json(doc) : json({ code: "E2078", message: "no schema registered" }, 404);
    }
    if (method === "GET" && path === "/api/schemas") return json({ schemas: Object.values(schemas) });
    for (const name of Object.keys(schemas)) {
      const collection = name === "work_orders" ? "work-orders" : name;
      if (path === `/api/${collection}` && (method === "GET" || method === "POST")) return json(page);
    }
    return new Response("404 page not found\n", { status: 404, headers: { "Content-Type": "text/plain; charset=utf-8" } });
  });
}

describe("routes", () => {
  const products: ResourceSchema = { ...schema, resource: "products" };
  const workOrders: ResourceSchema = { ...schema, resource: "work_orders" };
  const atAPI = (fetchFn: typeof globalThis.fetch) =>
    createClient({ baseURL: "https://api.test/api", fetch: fetchFn, validateBeforeSend: true });

  it("reaches the list and the schema routes of the example server with default options", async () => {
    const server = exampleServer({ products });
    const res = atAPI(server.fetch).resource<WireItem>("products");
    await expect(res.list({ filter: { name: "alpha" } })).resolves.toBeDefined();
    await expect(res.schema()).resolves.toEqual(products);
    expect(server.calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual(["GET /api/products/schema", "GET /api/products"]);
  });

  it("fetches a collection's schema under its registry name", async () => {
    const server = exampleServer({ work_orders: workOrders });
    const res = atAPI(server.fetch).resource<WireItem>("work-orders", { schema: "work_orders" });
    await expect(res.list({ filter: { name: "alpha" } })).resolves.toBeDefined();
    expect(server.calls.map((c) => c.url.pathname)).toEqual(["/api/work_orders/schema", "/api/work-orders"]);
  });

  it("caches schemas() by registry name, so a handle's schema() sends nothing", async () => {
    const server = exampleServer({ products, work_orders: workOrders });
    const c = atAPI(server.fetch);
    await c.schemas();
    await c.resource<WireItem>("products").schema();
    await c.resource<WireItem>("work-orders", { schema: "work_orders" }).schema();
    expect(server.calls.map((call) => call.url.pathname)).toEqual(["/api/schemas"]);
  });

  it("refuses a schema document for another resource than requested", async () => {
    const server = fakeServer(() => json(schema));
    await expect(client(server.fetch).resource<WireItem>("items").schema()).rejects.toMatchObject({
      name: "SchemaResourceError", requested: "items", resource: "wire_items",
    });
    await expect(client(server.fetch).resource<WireItem>("items").schema()).rejects.toBeInstanceOf(SchemaResourceError);
  });

  it("joins segments with one slash and puts schemas under schemaPrefix", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(schema) : json(page(0))));
    const c = createClient({ baseURL: "https://api.test/v1/", fetch: server.fetch, schemaPrefix: "/meta/", validateBeforeSend: true });
    await c.resource<WireItem>("/items/", { schema: "wire_items" }).list();
    await c.resource<WireItem>("shop/items", { schema: "wire_items" }).list();
    expect(server.calls.map((call) => call.url.pathname)).toEqual(["/v1/meta/wire_items/schema", "/v1/items", "/v1/shop/items"]);
  });

  it("defaults a nested path's registry name to its last segment", async () => {
    const server = fakeServer(() => json(schema));
    await client(server.fetch).resource<WireItem>("shop/wire_items").schema();
    expect(server.calls[0]?.url.pathname).toBe("/wire_items/schema");
  });

  it("refuses an empty path or schema name", () => {
    const c = client(fakeServer(() => json({})).fetch);
    expect(() => c.resource("")).toThrow(RangeError);
    expect(() => c.resource("/")).toThrow(RangeError);
    expect(() => c.resource("items", { schema: "" })).toThrow(RangeError);
    expect(() => c.resource("items", { schema: "a/b" })).toThrow(RangeError);
  });
});

describe("headers", () => {
  const empty = { items: [], page_info: { has_next_page: false, has_prev_page: false } };

  it("lets a lower-case override replace a default rather than join it", async () => {
    const server = fakeServer(() => json(empty));
    const c = createClient({ baseURL: "https://api.test", fetch: server.fetch, headers: { accept: "application/json" } });
    await c.resource<WireItem>("items").list({}, { method: "POST", headers: { "content-type": "application/json; charset=utf-8" } });
    expect(server.calls[0]?.headers.get("Content-Type")).toBe("application/json; charset=utf-8");
    expect(server.calls[0]?.headers.get("Accept")).toBe("application/json");
  });

  it("sends Content-Type only with a body", async () => {
    const server = fakeServer(({ method }) => (method === "OPTIONS" ? json({ facets: {} }) : json(empty)));
    const items = client(server.fetch).resource<WireItem>("items");
    await items.list();
    await items.facets(["name"]);
    await items.list({}, { method: "POST" });
    expect(server.calls.map((c) => c.headers.get("Content-Type"))).toEqual([null, null, "application/json"]);
  });
});

describe("2xx responses without a JSON body", () => {
  const cases: { name: string; response: () => Response; status: number }[] = [
    { name: "a 204", response: () => new Response(null, { status: 204 }), status: 204 },
    { name: "an empty 200", response: () => new Response("", { status: 200 }), status: 200 },
    { name: "an HTML 200", response: () => new Response("<html>ok</html>", { status: 200, headers: { "Content-Type": "text/html" } }), status: 200 },
  ];

  it.each(cases)("reject $name with a ResponseError carrying the status", async ({ response, status }) => {
    const err = await client(fakeServer(response).fetch).resource<WireItem>("items").patch("a", { name: "b" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ResponseError);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status, code: "", url: "https://api.test/items/a" });
  });
});

describe("timeout", () => {
  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, 1.5, 2 ** 32])("refuses %s", (timeout) => {
    expect(() => createClient({ baseURL: "https://api.test", timeout })).toThrow(RangeError);
  });
  it("takes a positive integer up to 2^32 - 1", () => {
    expect(() => createClient({ baseURL: "https://api.test", timeout: 1 })).not.toThrow();
    expect(() => createClient({ baseURL: "https://api.test", timeout: 2 ** 32 - 1 })).not.toThrow();
  });
});

describe("list members", () => {
  const empty = { items: [], page_info: { has_next_page: false, has_prev_page: false } };
  const p = (name: string): string => wire.params[name] ?? `missing ${name}`;

  it("sends fields on both transports", async () => {
    const server = fakeServer(() => json(empty));
    const items = client(server.fetch).resource<WireItem>("items");
    await items.list({ fields: ["id", "name"] });
    await items.list({ fields: ["id", "name"] }, { method: "POST" });
    expect(server.calls[0]?.url.searchParams.get(p("ParamFields"))).toBe("id,name");
    expect(server.calls[1]?.body).toEqual({ [p("ParamFields")]: ["id", "name"] });
  });

  it("leaves zero members out, as the Go client does", async () => {
    const server = fakeServer(() => json(empty));
    const items = client(server.fetch).resource<WireItem>("items");
    const zero = { sort: [], pageSize: 0, include: [], facets: [], fields: [], search: "", cursor: "" };
    await items.list(zero);
    await items.list(zero, { method: "POST" });
    expect(server.calls[0]?.url.search).toBe("");
    expect(server.calls[1]?.body).toEqual({});
  });
});

describe("requests the server would misroute", () => {
  // An empty segment is no segment, and URL parsing drops "." and "..",
  // escaped or not: each would PATCH another path than the row's.
  it("refuses a key no path segment carries, and patchMany without ids, before sending", async () => {
    const server = fakeServer(() => json({ updated: [] }));
    const items = client(server.fetch).resource<WireItem>("items");
    for (const key of ["", ".", "..", Number.NaN, Number.POSITIVE_INFINITY]) {
      await expect(items.patch(key, { name: "a" })).rejects.toBeInstanceOf(RangeError);
    }
    await expect(client(server.fetch).resource<WireStock, [string, string]>("stock").patch(["w1", ".."], { qty: 1 })).rejects.toBeInstanceOf(RangeError);
    await expect(items.patch([] as unknown as string, { name: "a" })).rejects.toBeInstanceOf(RangeError);
    await expect(items.patch({} as unknown as string, { name: "a" })).rejects.toBeInstanceOf(TypeError);
    await expect(items.patchMany([], { name: "a" })).rejects.toBeInstanceOf(RangeError);
    expect(server.calls).toHaveLength(0);
  });
});

describe("validation before sending", () => {
  const facetServer = () => fakeServer(({ method, url }) => {
    if (url.pathname.endsWith("/wire_parts/schema")) return json(related["wire_parts"]);
    if (url.pathname.endsWith("/schema")) return json(schema);
    return json(method === "OPTIONS" ? { facets: {} } : page(0));
  });

  it("validates facets' fields and their filter's relation predicates as list does", async () => {
    const server = facetServer();
    const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    await expect(items.facets(["nope" as "name"])).rejects.toMatchObject({ code: "E2071" });
    await expect(items.facets(["labels"])).rejects.toMatchObject({ code: "E2071" });
    await expect(items.facets(["name"], { filter: { parts: { $some: { nope: 1 } } } as never })).rejects.toBeInstanceOf(SchemaValidationError);
    await expect(items.facets(["name"])).resolves.toEqual({});
    expect(server.calls.filter((c) => c.method === "OPTIONS")).toHaveLength(1);
  });

  it("refuses a search on a resource whose schema has no search", async () => {
    const server = facetServer();
    const items = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    await expect(items.list({ search: "drill" })).rejects.toMatchObject({ code: "E2067", field: "search" });
    await expect(items.facets(["name"], { search: "drill" })).rejects.toMatchObject({ code: "E2067" });
    await expect(items.list({ search: "  " })).resolves.toBeDefined();
  });
});

describe("methods detached from their handle", () => {
  const updated = exchange<PatchResult<WireItem>>("patch").response.body;
  const p = (name: string): string => wire.params[name] ?? `missing ${name}`;

  // walkServer answers the schema, the recorded keyset walk by cursor,
  // patches and facets.
  const walkServer = () => fakeServer(({ method, url }) => {
    if (url.pathname.endsWith("/schema")) return json(schema);
    if (url.pathname.endsWith("/schemas")) return json(exchange("schemas").response.body);
    if (method === "PATCH") return json(updated);
    if (method === "OPTIONS") return json({ facets: {} });
    const cursor = url.searchParams.get(p("ParamCursor")) ?? "";
    const recorded = wire.list.find((c) => (c.request.query?.[p("ParamCursor")]?.[0] ?? "") === cursor);
    return recorded ? json(recorded.response.body) : json({ code: "E2082", message: "invalid cursor" }, 422);
  });
  const sent = (calls: Call[]) => calls.map((c) => ({ method: c.method, url: c.url.href, body: c.body }));
  const walkOpts = { sort: wire.sort, pageSize: wire.page_size };
  const notEcho = { filter: { name: { $ne: "echo" } } };

  async function collect<R>(pages: AsyncIterable<R>): Promise<R[]> {
    const out: R[] = [];
    for await (const page of pages) out.push(page);
    return out;
  }

  // A query library or retry wrapper: it calls the function it is given
  // with no receiver.
  const wrapped = <A extends unknown[], R>(f: (...args: A) => R) => (...args: A): R => f(...args);

  it.each([false, true])("send what calls through the handle send (validateBeforeSend %s)", async (validate) => {
    const control = walkServer();
    const handle = client(control.fetch, validate).resource<WireItem>("items", { schema: "wire_items" });
    const want = [
      await handle.list({ ...notEcho, sort: "name" }),
      ...(await collect(handle.listAll(walkOpts))),
      await handle.patch("b", { name: "bravo" }),
      await handle.patchMany(["b", "d"], { name: "bravo" }),
      await handle.facets(["name"], notEcho),
      (await handle.validator()).filterableFields(),
      await handle.schema(),
      await handle.request<PatchResult<WireItem>>("PATCH", { key: "b", body: { name: "bravo" } }),
      await outcome(handle.request<undefined>("DELETE", { key: "b", response: "none" })),
    ];

    const server = walkServer();
    const { list, listAll, patch, patchMany, facets, validator, schema: schemaOf, request } =
      client(server.fetch, validate).resource<WireItem>("items", { schema: "wire_items" });
    const got = [
      await wrapped(list)({ ...notEcho, sort: "name" }),
      ...(await collect(wrapped(listAll)(walkOpts))),
      await patch("b", { name: "bravo" }),
      await wrapped(patchMany)(["b", "d"], { name: "bravo" }),
      await facets(["name"], notEcho),
      (await validator()).filterableFields(),
      await schemaOf(),
      await wrapped(request)<PatchResult<WireItem>>("PATCH", { key: "b", body: { name: "bravo" } }),
      await outcome(request<undefined>("DELETE", { key: "b", response: "none" })),
    ];

    expect(got).toEqual(want);
    expect(sent(server.calls)).toEqual(sent(control.calls));
  });

  it("include the client's own", async () => {
    const server = walkServer();
    const { resource, schemas, request } = client(server.fetch, true);
    await expect(wrapped(resource<WireItem>)("items", { schema: "wire_items" }).list({ sort: "name" })).resolves.toEqual(page(0));
    await expect(wrapped(schemas)()).resolves.toEqual(Object.values(related));
    await expect(wrapped(request)<ListResult<WireItem>>("GET", "items")).resolves.toEqual(page(0));
    expect(server.calls.map((c) => c.url.pathname)).toEqual(["/wire_items/schema", "/items", "/schemas", "/items"]);
  });

  it.each([false, true])("refuse what calls through the handle refuse (validateBeforeSend %s)", async (validate) => {
    const server = walkServer();
    const { list, patch, patchMany, facets, request } = client(server.fetch, validate).resource<WireItem>("items", { schema: "wire_items" });
    await expect(patch("", { name: "bravo" })).rejects.toBeInstanceOf(RangeError);
    await expect(patchMany([], { name: "bravo" })).rejects.toBeInstanceOf(RangeError);
    await expect(request("GET", { key: "" })).rejects.toBeInstanceOf(RangeError);
    await expect(request("PUT", { key: "b", body: { score: Number.NaN } })).rejects.toBeInstanceOf(NonFiniteNumberError);
    if (validate) {
      await expect(list({ filter: { name: "zulu" } })).rejects.toMatchObject({ code: "E2087" });
      await expect(facets(["labels"])).rejects.toMatchObject({ code: "E2071" });
      await expect(patch("a", { name: null })).rejects.toMatchObject({ code: "E2077" });
      await expect(patchMany(["a"], { name: null })).rejects.toMatchObject({ code: "E2077" });
    }
    expect(server.calls.filter((c) => !c.url.pathname.endsWith("/schema"))).toHaveLength(0);
  });

  it("iterate lazily: listAll sends nothing until a page is asked for", async () => {
    const server = walkServer();
    const { listAll } = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    const pages = listAll(walkOpts);
    expect(server.calls).toHaveLength(0);
    const first = await pages.next();
    if (first.done) throw new Error("listAll yielded no page");
    expect(first.value).toEqual(page(0));
    expect(first.value.page_info.has_next_page).toBe(true);
    expect(server.calls.map((c) => c.url.pathname)).toEqual(["/wire_items/schema", "/items"]);
  });

  it("share one schema fetch between concurrent validating calls", async () => {
    const server = walkServer();
    const { list, patch, patchMany, facets, validator } = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    await Promise.all([list({ sort: "name" }), patch("b", { name: "bravo" }), patchMany(["b"], { name: "bravo" }), facets(["name"]), validator()]);
    expect(server.calls.filter((c) => c.url.pathname.endsWith("/schema"))).toHaveLength(1);
  });

  it("end a validating list's wait on its abort, sending nothing", async () => {
    const server = walkServer();
    const { list, listAll } = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    await expect(list({ sort: "name" }, { signal: AbortSignal.abort() })).rejects.toMatchObject({ name: "AbortError" });
    await expect(listAll(walkOpts, { signal: AbortSignal.abort() }).next()).rejects.toMatchObject({ name: "AbortError" });
    expect(server.calls).toHaveLength(0);
  });
});

// JSON has no NaN or Infinity, and JSON.stringify writes them as null, which
// a merge patch reads as the member's removal and a filter as a NULL test.
describe("non-finite numbers", () => {
  interface Scored extends WireItem {
    score: number | null;
  }
  // The recorded wire_items schema with a nullable float, which null clears.
  const recordedPatch = schema.patch;
  if (!recordedPatch) throw new Error("the recorded schema has no patch section");
  const scored: ResourceSchema = {
    ...schema,
    patch: { ...recordedPatch, paths: { ...recordedPatch.paths, "/score": { allowed_ops: ["replace", "remove"], data_type: "float", nullable: true, number_kind: "float64" } } },
  };
  const empty = { items: [], page_info: { has_next_page: false, has_prev_page: false } };
  const scoreServer = () => fakeServer(({ method, url }) => {
    if (url.pathname.endsWith("/schema")) return json(scored);
    if (method === "PATCH") return json({ updated: [] });
    if (method === "OPTIONS") return json({ facets: {} });
    return json(empty);
  });
  const scores = (fetchFn: typeof globalThis.fetch, validate: boolean) =>
    client(fetchFn, validate).resource<Scored>("items", { schema: "wire_items" });

  const refused: { name: string; input: PatchInput<Scored>; pointer: string; value: number }[] = [
    { name: "a nullable float", input: { score: Number.NaN }, pointer: "/score", value: Number.NaN },
    { name: "a JSON document member", input: { labels: { weight: Number.POSITIVE_INFINITY } }, pointer: "/labels/weight", value: Number.POSITIVE_INFINITY },
    { name: "an array element in a JSON document", input: { labels: { readings: [1, Number.NEGATIVE_INFINITY] } }, pointer: "/labels/readings/1", value: Number.NEGATIVE_INFINITY },
  ];

  describe.each([false, true])("with validateBeforeSend %s", (validate) => {
    it.each(refused)("refuse $name in patch and patchMany, sending nothing", async ({ input, pointer, value }) => {
      const server = scoreServer();
      const items = scores(server.fetch, validate);
      await expect(items.patch("one", input)).rejects.toMatchObject({ name: "NonFiniteNumberError", payload: "patch", pointer, value });
      await expect(items.patchMany(["one", "two"], input)).rejects.toMatchObject({ name: "NonFiniteNumberError", payload: "patch", pointer, value });
      await expect(items.patch("one", input)).rejects.toBeInstanceOf(NonFiniteNumberError);
      expect(server.calls).toHaveLength(0);
    });

    it("send explicit null, finite numbers and nested JSON unchanged", async () => {
      const server = scoreServer();
      const items = scores(server.fetch, validate);
      const inputs: PatchInput<Scored>[] = [
        { score: null },
        { score: 0.5 },
        { score: Number.MAX_VALUE, labels: { weight: 2.5, readings: [1, -2], nested: { gone: null } } },
      ];
      for (const input of inputs) {
        await items.patch("one", input);
        await items.patchMany(["one"], input);
      }
      expect(server.calls.filter((c) => c.method === "PATCH").map((c) => c.body)).toEqual(inputs.flatMap((input) => [input, input]));
    });

    it("refuse one in a filter on every transport, sending nothing", async () => {
      const server = scoreServer();
      const items = scores(server.fetch, validate);
      const filter = { score: { $lt: Number.NaN } };
      const nan = { name: "NonFiniteNumberError", payload: "filter", pointer: "/score/$lt" };
      await expect(items.list({ filter }, { method: "GET" })).rejects.toMatchObject(nan);
      await expect(items.list({ filter }, { method: "POST" })).rejects.toMatchObject(nan);
      await expect(items.listAll({ filter }).next()).rejects.toMatchObject(nan);
      await expect(items.facets(["name"], { filter })).rejects.toMatchObject(nan);
      expect(server.calls).toHaveLength(0);
    });
  });
});

describe("what validation and the transports read", () => {
  interface Seen extends WireItem {
    seen: Date | string | null;
  }
  // The recorded wire_items schema with a nullable timestamp, filterable and patchable.
  const recordedPatch = schema.patch;
  const recordedFilter = schema.filter;
  if (!recordedPatch || !recordedFilter) throw new Error("the recorded schema has no patch or filter section");
  const seenSchema: ResourceSchema = {
    ...schema,
    filter: { ...recordedFilter, fields: { ...recordedFilter.fields, seen: { name: "seen", data_type: "timestamp", operators: ["eq", "gte", "lt", "null"] } } },
    patch: { ...recordedPatch, paths: { ...recordedPatch.paths, "/seen": { allowed_ops: ["replace", "remove"], data_type: "timestamp", nullable: true } } },
  };
  const empty = { items: [], page_info: { has_next_page: false, has_prev_page: false } };
  const seenServer = () => fakeServer(({ method, url }) => {
    if (url.pathname.endsWith("/schema")) return json(seenSchema);
    if (method === "PATCH") return json({ updated: [] });
    if (method === "OPTIONS") return json({ facets: {} });
    return json(empty);
  });
  const items = (fetchFn: typeof globalThis.fetch, validate: boolean) =>
    client(fetchFn, validate).resource<Seen>("items", { schema: "wire_items" });
  const at = new Date("2026-01-02T03:04:05.000Z");
  // A value as a caller whose compiler admits undefined members passes it.
  const loose = (value: object): never => value as never;

  describe.each([false, true])("with validateBeforeSend %s", (validate) => {
    it("refuses an invalid Date in a patch or filter, sending nothing", async () => {
      const server = seenServer();
      const r = items(server.fetch, validate);
      const invalid = new Date(Number.NaN);
      await expect(r.patch("one", { seen: invalid })).rejects.toMatchObject({ name: "UnencodableValueError", payload: "patch", pointer: "/seen", value: invalid });
      await expect(r.list({ filter: { seen: { $gte: invalid } } })).rejects.toBeInstanceOf(UnencodableValueError);
      await expect(r.facets(["name"], { filter: { seen: { $gte: invalid } } })).rejects.toBeInstanceOf(UnencodableValueError);
      expect(server.calls).toHaveLength(0);
    });

    it("sends a Date as its RFC 3339 text and leaves undefined members out", async () => {
      const server = seenServer();
      const r = items(server.fetch, validate);
      // exactOptionalPropertyTypes refuses undefined members; a caller without it, or JavaScript, sends them.
      await r.patch("one", loose({ seen: at, name: undefined }));
      await r.list({ filter: loose({ seen: { $gte: at }, name: undefined }) }, { method: "POST" });
      await r.list({ filter: loose({ name: undefined }) });
      const sent = server.calls.filter((c) => !c.url.pathname.endsWith("/schema"));
      expect(sent[0]?.body).toEqual({ seen: "2026-01-02T03:04:05.000Z" });
      expect(sent[1]?.body).toEqual({ filter: { seen: { $gte: "2026-01-02T03:04:05.000Z" } } });
      expect(sent[2]?.url.searchParams.has("filter")).toBe(false);
    });
  });

  it("validates the JSON the request sends, not the JavaScript value", async () => {
    const server = seenServer();
    const r = items(server.fetch, true);
    // Each member below is wrong only for a value the wire never carries.
    await expect(r.list({ filter: { seen: { $gte: at } } })).resolves.toEqual(empty);
    await expect(r.facets(["name"], { filter: { seen: { $lt: at } } })).resolves.toEqual({});
    await expect(r.patch("one", { seen: at })).resolves.toEqual({ updated: [] });
    await expect(r.patchMany(["one"], loose({ seen: at, name: undefined }))).resolves.toEqual({ updated: [] });
    // And a wrong value is still refused: a timestamp is text.
    await expect(r.patch("one", { seen: 5 as unknown as string })).rejects.toBeInstanceOf(SchemaValidationError);
  });
});

describe("the schema cache", () => {
  interface Colored extends WireItem {
    color: string;
  }
  const recordedFilter = schema.filter;
  if (!recordedFilter) throw new Error("the recorded schema has no filter section");
  const withColor: ResourceSchema = {
    ...schema,
    filter: { ...recordedFilter, fields: { ...recordedFilter.fields, color: { name: "color", data_type: "string", operators: ["eq"] } } },
  };
  const empty = { items: [], page_info: { has_next_page: false, has_prev_page: false } };

  it("validates against the document schemas() cached last", async () => {
    let served = schema;
    const server = fakeServer(({ url }) => {
      if (url.pathname.endsWith("/schemas")) return json({ schemas: [served] });
      if (url.pathname.endsWith("/schema")) return json(served);
      return json(empty);
    });
    const c = client(server.fetch, true);
    const r = c.resource<Colored>("items", { schema: "wire_items" });
    await expect(r.list({ filter: { color: "red" } })).rejects.toMatchObject({ code: "E2071" });
    served = withColor;
    await c.schemas();
    await expect(r.list({ filter: { color: "red" } })).resolves.toEqual(empty);
    expect((await r.validator()).filterableFields()).toContain("color");
  });

  it("hands out a frozen document, so no caller changes what validation reads", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(schema) : json(empty)));
    const r = client(server.fetch, true).resource<WireItem>("items", { schema: "wire_items" });
    const doc = await r.schema();
    expect(Object.isFrozen(doc)).toBe(true);
    expect(Object.isFrozen(doc.filter?.fields)).toBe(true);
    expect(() => {
      delete (doc.filter?.fields as Record<string, unknown>)["name"];
    }).toThrow(TypeError);
    await expect(r.list({ filter: { name: "alpha" } })).resolves.toEqual(empty);
  });
});

describe("2xx answers of another shape", () => {
  const cases: { name: string; call: (c: Client) => Promise<unknown>; answer: unknown; reason: string }[] = [
    { name: "a schema of null", call: (c) => c.resource("items", { schema: "wire_items" }).schema(), answer: null, reason: "not an object naming its resource" },
    { name: "a schema list without schemas", call: (c) => c.schemas(), answer: {}, reason: "no schemas array" },
    { name: "a schema list holding null", call: (c) => c.schemas(), answer: { schemas: [null] }, reason: "no schemas array" },
    { name: "a page without page_info", call: (c) => c.resource<WireItem>("items").list(), answer: { items: [] }, reason: "page_info" },
    { name: "a page whose items are no array", call: (c) => c.resource<WireItem>("items").list(), answer: { items: {}, page_info: { has_next_page: false } }, reason: "no items array" },
    { name: "a page with a numeric cursor", call: (c) => c.resource<WireItem>("items").list(), answer: { items: [], page_info: { has_next_page: true, end_cursor: 5 } }, reason: "end_cursor is not a string" },
    { name: "a patch answer without updated", call: (c) => c.resource<WireItem>("items").patch("a", { name: "b" }), answer: { ok: true }, reason: "no updated array" },
    { name: "a facets answer of facets that are no object", call: (c) => c.resource<WireItem>("items").facets(["name"]), answer: { facets: [] }, reason: "not an object of facets" },
  ];

  it.each(cases)("reject $name with a ResponseError", async ({ call, answer, reason }) => {
    const err = await call(client(fakeServer(() => json(answer)).fetch)).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ResponseError);
    expect(err).toMatchObject({ status: 200, code: "" });
    expect((err as Error).message).toContain(reason);
  });
});

describe("listAll over cursors that do not advance", () => {
  const pageWith = (info: Record<string, unknown>) => json({ items: [], page_info: { has_prev_page: false, ...info } });

  it("refuses a cursor that repeats the one it was asked with", async () => {
    const server = fakeServer(() => pageWith({ has_next_page: true, end_cursor: "same" }));
    const pages = client(server.fetch).resource<WireItem>("items").listAll();
    await pages.next();
    await pages.next();
    await expect(pages.next()).rejects.toMatchObject({ name: "ResponseError", status: 200 });
    expect(server.calls).toHaveLength(2);
  });

  it("refuses a next page it gives no cursor for, rather than ending early", async () => {
    const server = fakeServer(() => pageWith({ has_next_page: true }));
    const pages = client(server.fetch).resource<WireItem>("items").listAll();
    await pages.next();
    await expect(pages.next()).rejects.toThrow("has_next_page is true without an end_cursor");
  });
});
