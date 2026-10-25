// Client ⇔ server wire contract. Responses come from
// testdata/sdk_wire.json, emitted by the Go parse package (a real keyset
// walk serialized as filter.Page, and a registry schema response), so these
// tests hold the SDK to the server's shapes rather than to its own types.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { createClient } from "./client.js";
import { ApiError, SchemaValidationError, SchemaVersionError } from "./core/errors.js";
import type { ListResult } from "./filter/types.js";
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
  body: unknown;
}

// fakeServer answers from handler and records every request.
function fakeServer(handler: (call: Call) => Response) {
  const calls: Call[] = [];
  const fetchFn: typeof globalThis.fetch = (input, init) => {
    const call: Call = {
      method: init?.method ?? "GET",
      url: new URL(input instanceof Request ? input.url : String(input)),
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
  it("reads the server error body", async () => {
    const server = fakeServer(() => json({ code: "E2071", message: "field 'x' is not filterable", details: { field: "x" } }, 422));
    const err = await client(server.fetch).resource<WireItem>("items").list().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: "E2071", message: "field 'x' is not filterable" });
  });

  it("keeps the status when the body is not JSON", async () => {
    const server = fakeServer(() => new Response("<html>bad gateway</html>", { status: 502, statusText: "Bad Gateway" }));
    const err = await client(server.fetch).resource<WireItem>("items").list().catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 502, code: "", message: "Bad Gateway" });
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
