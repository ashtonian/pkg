// The README's Server Contract table is the client's half of the contract
// with a Go server: it has one row per public method, and the example
// server's Routes table serves every request the table sends. Routes no
// client method calls, such as the readiness probe, name no call.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { createClient } from "./client.js";

const read = (path: string): string => readFileSync(new URL(path, import.meta.url), "utf8");

// rows are the cells of each body row of the table under heading.
function rows(markdown: string, heading: string): string[][] {
  const start = markdown.indexOf(`\n## ${heading}\n`);
  if (start < 0) throw new Error(`no ${heading} section`);
  const next = markdown.indexOf("\n## ", start + 1);
  const section = markdown.slice(start, next < 0 ? undefined : next);
  const table = section.split("\n").filter((line) => line.startsWith("|"));
  return table.slice(2).map((line) => line.slice(1, -1).split(" | ").map((cell) => cell.trim()));
}

// firstCode is the first code span of a cell.
const firstCode = (cell: string | undefined): string => /`([^`]+)`/.exec(cell ?? "")?.[1] ?? "";

const httpMethod = /^(GET|POST|PATCH|OPTIONS) /;

const contract = rows(read("../README.md"), "Server Contract").map(([call, request]) => ({
  method: /^(\w+)\(/.exec(firstCode(call))?.[1] ?? "",
  sends: httpMethod.exec(firstCode(request))?.[1],
}));

describe("server contract table", () => {
  it("has one row per public client method", () => {
    const client = createClient({ baseURL: "https://api.test", fetch: () => Promise.reject(new Error("no request is sent")) });
    const methods = [...Object.keys(client), ...Object.keys(client.resource("items"))];
    expect(new Set(contract.map((row) => row.method))).toEqual(new Set(methods));
  });

  it("is served by the example server", () => {
    const sent = contract.flatMap((row) => (row.sends ? [`${row.method} ${row.sends}`] : []));
    const served = rows(read("../../../examples/server/README.md"), "Routes").flatMap(([route, call]) => {
      const method = firstCode(call);
      const verb = httpMethod.exec(firstCode(route))?.[1] ?? "";
      return method ? [`${method} ${verb}`] : [];
    });
    expect(sent.length).toBeGreaterThan(0);
    expect(new Set(served)).toEqual(new Set(sent));
  });
});
