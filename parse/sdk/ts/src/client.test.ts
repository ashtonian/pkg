// Client ⇔ server wire contract. Responses come from
// testdata/sdk_wire.json, emitted by the Go parse package (a real keyset
// walk serialized as filter.Page, and a registry schema response), so these
// tests hold the SDK to the server's shapes rather than to its own types.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { createClient } from "./client.js";
// ApiError and the problem types come through the package's public entry points.
import { ApiError, SchemaValidationError, SchemaVersionError } from "./core/index.js";
import type { ListResult } from "./filter/types.js";
import type { FieldError, Problem } from "./index.js";
import { type ResourceSchema, SCHEMA_VERSION } from "./schema.js";

interface WireItem {
  id: string;
  name: string;
  labels: Record<string, unknown>;
}

interface Wire {
  params: Record<string, string>;
  page_size: number;
  sort: string;
  list: { cursor: string; response: ListResult<WireItem> }[];
  schema: ResourceSchema;
  related: Record<string, ResourceSchema>;
}

const wire = JSON.parse(
  readFileSync(new URL("../../../testdata/sdk_wire.json", import.meta.url), "utf8"),
) as Wire;

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

function client(fetchFn: typeof globalThis.fetch, validateBeforeSend = false) {
  return createClient({ baseURL: "https://api.test", fetch: fetchFn, validateBeforeSend });
}

describe("listAll", () => {
  it("follows page_info.end_cursor through the Go-emitted pages", async () => {
    const server = fakeServer(({ url }) => {
      const cursor = url.searchParams.get("cursor") ?? "";
      const exchange = wire.list.find((e) => e.cursor === cursor);
      return exchange ? json(exchange.response) : json({ code: "E2082", message: "invalid cursor" }, 422);
    });
    const items = client(server.fetch).resource<WireItem>("items");

    const names: string[] = [];
    for await (const page of items.listAll({ sort: wire.sort, pageSize: wire.page_size })) {
      names.push(...page.items.map((i) => i.name));
    }

    expect(names).toEqual(["alpha", "bravo", "charlie", "delta", "echo"]);
    expect(server.calls.map((c) => c.url.searchParams.get("cursor") ?? "")).toEqual(wire.list.map((e) => e.cursor));
    for (const c of server.calls) {
      expect(c.url.searchParams.get("page_size")).toBe("2");
      expect(c.url.searchParams.get("sort")).toBe("name");
    }
  });

  it("surfaces include projections and facet counts", async () => {
    const first = wire.list[0]?.response;
    const items = client(fakeServer(() => json(first)).fetch).resource<WireItem>("items");
    const page = await items.list();
    expect(page._relations?.["tags"]?.["b"]).toEqual(["t1", "t2"]);
    expect(page._truncated?.["tags"]).toEqual(["b"]);
    expect(page.facets?.["name"]?.map((b) => b.key)).toEqual(["alpha", "bravo", "charlie", "delta", "echo"]);
  });
});

describe("list", () => {
  it("sends the server's query parameters", async () => {
    const server = fakeServer(() => json({ items: [], page_info: { has_next_page: false, has_prev_page: false } }));
    await client(server.fetch).resource<WireItem>("items").list({
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
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(wire.schema) : json(wire.list[0]?.response)));
    const items = client(server.fetch, true).resource<WireItem>("items");

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

describe("relation filters", () => {
  it("validates a nested filter against the target's Go-emitted schema", async () => {
    const server = fakeServer(({ url }) => {
      if (url.pathname.endsWith("/wire_parts/schema")) return json(wire.related["wire_parts"]);
      if (url.pathname.endsWith("/schema")) return json(wire.schema);
      return json(wire.list[0]?.response);
    });
    const items = client(server.fetch, true).resource<WireItem>("items");

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
  const parseBody = { code: "E2071", message: "field 'x' is not filterable", details: { field: "x" } };
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
    const err = await client(fakeServer(response).fetch).resource<WireItem>("items").list().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject(want);
  });

  it("asks for problem documents as well as JSON", async () => {
    const server = fakeServer(() => json(wire.list[0]?.response));
    await client(server.fetch).resource<WireItem>("items").list();
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
      return json(wire.schema);
    });
    const items = client(server.fetch).resource<WireItem>("items");

    await expect(items.schema()).rejects.toMatchObject({ status: 503 });
    const [a, b] = await Promise.all([items.schema(), items.schema()]);
    expect(a).toEqual(wire.schema);
    expect(b).toBe(a);
    expect(server.calls).toHaveLength(2);
    await items.schema();
    expect(server.calls).toHaveLength(2);
  });

  describe("a shared fetch", () => {
    interface Held {
      signal: AbortSignal;
      release: () => void;
    }

    // heldServer answers each request only when released, and rejects one
    // whose signal aborts first with the signal's reason, as fetch does.
    function heldServer() {
      const requests: Held[] = [];
      const fetchFn: typeof globalThis.fetch = (_input, init) => {
        const signal = init?.signal;
        if (!signal) throw new Error("request sent without a signal");
        return new Promise<Response>((resolve, reject) => {
          signal.addEventListener("abort", () => {
            reject(signal.reason as Error);
          }, { once: true });
          requests.push({ signal, release: () => { resolve(json(wire.schema)); } });
        });
      };
      return { requests, fetch: fetchFn };
    }

    function request(server: { requests: Held[] }, i: number): Held {
      const held = server.requests[i];
      if (!held) throw new Error(`no request ${String(i)}`);
      return held;
    }

    it("survives the first caller's abort for a second caller", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items");
      const first = new AbortController();

      const a = items.schema({ signal: first.signal });
      const b = items.schema();
      first.abort();

      await expect(a).rejects.toMatchObject({ name: "AbortError" });
      expect(server.requests).toHaveLength(1);
      expect(request(server, 0).signal.aborted).toBe(false);
      request(server, 0).release();
      await expect(b).resolves.toEqual(wire.schema);
    });

    it("rejects only a second caller that aborts", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items");
      const second = new AbortController();

      const a = items.schema();
      const b = items.schema({ signal: second.signal });
      second.abort();

      await expect(b).rejects.toMatchObject({ name: "AbortError" });
      request(server, 0).release();
      await expect(a).resolves.toEqual(wire.schema);
      expect(server.requests).toHaveLength(1);
    });

    it("is cancelled when every caller aborts, and the next call refetches", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items");
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
      await expect(c).resolves.toEqual(wire.schema);
      await items.schema();
      expect(server.requests).toHaveLength(2);
    });

    it("sends nothing for a caller that has already aborted", async () => {
      const server = heldServer();
      const items = client(server.fetch).resource<WireItem>("items");
      await expect(items.schema({ signal: AbortSignal.abort() })).rejects.toMatchObject({ name: "AbortError" });
      expect(server.requests).toHaveLength(0);
    });

    it("ends a validating list's wait without failing the other caller's", async () => {
      const server = heldServer();
      const items = client(server.fetch, true).resource<WireItem>("items");
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
    expect(wire.schema.version).toBe(SCHEMA_VERSION);
  });

  it("refuses a document in a version it does not read, without caching it", async () => {
    const future = { ...wire.schema, version: SCHEMA_VERSION + 1 };
    const server = fakeServer(() => json(future));
    const items = client(server.fetch, true).resource<WireItem>("items");

    const err = await items.schema().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(SchemaVersionError);
    expect(err).toMatchObject({ resource: "wire_items", version: SCHEMA_VERSION + 1, supported: SCHEMA_VERSION });
    await expect(items.list({ sort: "name" })).rejects.toBeInstanceOf(SchemaVersionError);
    expect(server.calls).toHaveLength(2);
  });

  it("refuses a listing that holds a document in another version", async () => {
    const server = fakeServer(() => json({ schemas: [wire.schema, { ...wire.schema, resource: "other", version: 0 }] }));
    await expect(client(server.fetch).schemas()).rejects.toBeInstanceOf(SchemaVersionError);
  });
});

describe("patch", () => {
  const updated = { updated: [{ id: "b", name: "bravo", labels: {} }] };

  it("sends a merge patch to the entity", async () => {
    const server = fakeServer(() => json(updated));
    const res = await client(server.fetch).resource<WireItem>("items").patch("a/b", { name: "bravo", labels: null });
    expect(server.calls[0]).toMatchObject({ method: "PATCH", body: { name: "bravo", labels: null } });
    expect(server.calls[0]?.url.pathname).toBe("/items/a%2Fb");
    expect(res.updated).toHaveLength(1);
  });

  it("patches several entities through id[] parameters", async () => {
    const server = fakeServer(() => json(updated));
    await client(server.fetch).resource<WireItem>("items").patchMany(["a", "b"], { name: "bravo" });
    expect(server.calls[0]?.method).toBe("PATCH");
    expect(server.calls[0]?.url.pathname).toBe("/items");
    expect(server.calls[0]?.url.searchParams.getAll("id[]")).toEqual(["a", "b"]);
  });

  it("validates null against the Go-emitted schema", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(wire.schema) : json(updated)));
    const items = client(server.fetch, true).resource<WireItem>("items");
    // name is a non-nullable string: null is not one of its values, as the server answers.
    await expect(items.patch("a", { name: null })).rejects.toMatchObject({ code: "E2077" });
    await expect(items.patch("a", { id: "x" })).rejects.toMatchObject({ code: "E2076" });
    await expect(items.patch("a", { labels: null })).resolves.toBeDefined();
  });

  it("looks up field names as escaped JSON Pointer tokens", async () => {
    const server = fakeServer(({ url }) => (url.pathname.endsWith("/schema") ? json(wire.schema) : json(updated)));
    const items = client(server.fetch, true).resource<Record<string, unknown>>("items");
    // The Go schema holds "/unit~1si~0v" for the field named "unit/si~v".
    await expect(items.patch("a", { "unit/si~v": "kg" })).resolves.toBeDefined();
    await expect(items.patch("a", { "unit~1si~0v": "kg" })).rejects.toMatchObject({ code: "E2076" });
  });
});

describe("facets", () => {
  it("reads facet counts from OPTIONS ?facets=", async () => {
    const server = fakeServer(() => json({ fields: [], facets: { name: [{ key: "alpha", count: 2 }] } }));
    const res = await client(server.fetch).resource<WireItem>("items").facets(["name"], { filter: { name: { $ne: "echo" } } });
    expect(server.calls[0]?.method).toBe("OPTIONS");
    expect(server.calls[0]?.url.searchParams.get(wire.params["ParamFacets"] ?? "")).toBe("name");
    expect(server.calls[0]?.url.searchParams.get("filter")).toBe(JSON.stringify({ name: { $ne: "echo" } }));
    expect(res).toEqual({ name: [{ key: "alpha", count: 2 }] });
  });

  it("sends the search the counts describe", async () => {
    const server = fakeServer(() => json({ fields: [], facets: {} }));
    await client(server.fetch).resource<WireItem>("items").facets(["name"], { search: "cordless drill" });
    expect(server.calls[0]?.url.searchParams.get(wire.params["ParamSearch"] ?? "")).toBe("cordless drill");
  });
});
