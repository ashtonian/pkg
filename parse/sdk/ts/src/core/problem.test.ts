import { describe, expect, it } from "vitest";
import { hasProblemMembers, isProblemMediaType, type Problem, problemFieldErrors, readProblem } from "./problem.js";

describe("readProblem", () => {
  const cases: { name: string; doc: Record<string, unknown>; url?: string; want: Problem }[] = [
    {
      name: "defaults a missing type to about:blank",
      doc: { title: "Not Found" },
      want: { type: "about:blank", title: "Not Found", extensions: {} },
    },
    {
      name: "reads an empty type as about:blank",
      doc: { type: "" },
      url: "https://api.example.com/v1/items?token=secret",
      want: { type: "about:blank", extensions: {} },
    },
    {
      name: "drops standard members of the wrong type",
      doc: { type: null, title: 42, detail: ["x"], instance: {}, status: "404", balance: 30 },
      want: { type: "about:blank", extensions: { balance: 30 } },
    },
    {
      name: "keeps a status only when it is an HTTP status code",
      doc: { status: 422 },
      want: { type: "about:blank", status: 422, extensions: {} },
    },
    {
      name: "drops a fractional status",
      doc: { status: 422.5 },
      want: { type: "about:blank", extensions: {} },
    },
    {
      name: "drops a status outside 100-599",
      doc: { status: 600 },
      want: { type: "about:blank", extensions: {} },
    },
    {
      name: "resolves relative type and instance URIs against the response URL",
      doc: { type: "/probs/out-of-credit", instance: "accounts/12345/msgs/abc" },
      url: "https://api.example.com/v1/items?page=2",
      want: {
        type: "https://api.example.com/probs/out-of-credit",
        instance: "https://api.example.com/v1/accounts/12345/msgs/abc",
        extensions: {},
      },
    },
    {
      name: "leaves URIs as sent without a response URL",
      doc: { type: "/probs/out-of-credit", instance: "msgs/abc" },
      want: { type: "/probs/out-of-credit", instance: "msgs/abc", extensions: {} },
    },
    {
      name: "leaves about:blank and absolute types alone",
      doc: { type: "https://example.net/validation-error" },
      url: "https://api.example.com/v1/items",
      want: { type: "https://example.net/validation-error", extensions: {} },
    },
    {
      name: "drops an empty instance",
      doc: { instance: "" },
      url: "https://api.example.com/v1/items",
      want: { type: "about:blank", extensions: {} },
    },
  ];

  it.each(cases)("$name", ({ doc, url, want }) => {
    expect(readProblem(doc, url ?? "")).toEqual(want);
  });

  it("keeps a member named __proto__ as an extension", () => {
    const problem = readProblem(JSON.parse('{"__proto__": {"polluted": true}}') as Record<string, unknown>, "");
    expect(Object.keys(problem.extensions)).toEqual(["__proto__"]);
    expect(Object.getPrototypeOf(problem.extensions)).toBe(Object.prototype);
  });
});

describe("problemFieldErrors", () => {
  function problem(errors: unknown): Problem {
    return { type: "about:blank", extensions: { errors } };
  }

  it("reads the members each entry names", () => {
    expect(problemFieldErrors(problem([
      { pointer: "#/age", detail: "must be a positive integer" },
      { pointer: "/name", detail: "too long", code: "E2077", limit: 64 },
    ]))).toEqual([
      { pointer: "#/age", detail: "must be a positive integer" },
      { pointer: "/name", detail: "too long", code: "E2077" },
    ]);
  });

  it("drops members of the wrong type and entries that are not objects", () => {
    expect(problemFieldErrors(problem([{ pointer: 7, detail: "kept" }, "loose text", null, ["x"]]))).toEqual([{ detail: "kept" }]);
  });

  it("is empty without an errors array", () => {
    expect(problemFieldErrors({ type: "about:blank", extensions: {} })).toEqual([]);
    expect(problemFieldErrors(problem({ age: "must be a positive integer" }))).toEqual([]);
  });
});

describe("problem recognition", () => {
  it.each([
    ["application/problem+json", true],
    ["application/problem+json; charset=utf-8", true],
    ["Application/Problem+JSON", true],
    ["application/json", false],
    ["application/vnd.example.problem+json", false],
    [null, false],
  ])("media type %s is a problem: %s", (contentType, want) => {
    expect(isProblemMediaType(contentType)).toBe(want);
  });

  it.each([
    [{ type: "https://example.net/x" }, true],
    [{ type: "about:blank" }, true],
    [{ title: "Not Found", status: 404 }, true],
    [{ type: "error" }, false],
    [{ type: "/probs/x" }, false],
    [{ title: "Not Found" }, false],
    [{ status: 404 }, false],
    [{ title: "Not Found", status: 404.5 }, false],
    [{ status: "error", message: "x" }, false],
    [{ code: "E2071", message: "x" }, false],
  ])("members of %j are a problem: %s", (doc, want) => {
    expect(hasProblemMembers(doc)).toBe(want);
  });
});
