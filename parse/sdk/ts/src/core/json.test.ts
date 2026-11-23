import { describe, expect, it } from "vitest";

import { NonFiniteNumberError } from "./errors.js";
import { encodeJSON } from "./json.js";

describe("encodeJSON", () => {
  const shared = { kept: 1 };

  it.each([
    { name: "explicit null, at the top and in a document", value: { score: null, prefs: { label: null }, list: [null] } },
    { name: "a string and a key spelling null", value: { null: "null", note: "nullable" } },
    { name: "finite numbers, the extremes included", value: { n: [0, -0, 1.5, -2, Number.MAX_VALUE, Number.MIN_VALUE, Number.MAX_SAFE_INTEGER] } },
    { name: "nested documents and arrays", value: { prefs: { city: "Bath", units: ["metric", { scale: 2 }] }, ok: true } },
    { name: "a value with toJSON", value: { at: new Date(0) } },
    { name: "members JSON leaves out", value: { gone: undefined, fn: () => 1, kept: "x" } },
    { name: "an object reached twice", value: { a: shared, b: [shared] } },
  ])("writes $name as JSON.stringify does", ({ value }) => {
    expect(encodeJSON(value, "patch")).toBe(JSON.stringify(value));
  });

  it.each([
    { name: "a top-level member", value: { score: Number.NaN }, pointer: "/score", number: Number.NaN },
    { name: "a document member", value: { prefs: { score: Number.POSITIVE_INFINITY } }, pointer: "/prefs/score", number: Number.POSITIVE_INFINITY },
    { name: "an array element", value: { scores: [1, Number.NEGATIVE_INFINITY] }, pointer: "/scores/1", number: Number.NEGATIVE_INFINITY },
    { name: "a member of an array element", value: { stops: [{ at: 1 }, { at: Number.NaN }] }, pointer: "/stops/1/at", number: Number.NaN },
    { name: "a member after an object reached twice", value: { a: shared, b: [shared, { n: Number.NaN }] }, pointer: "/b/1/n", number: Number.NaN },
    { name: "a member whose keys a pointer escapes", value: { "a/b": { "c~d": Number.NaN } }, pointer: "/a~1b/c~0d", number: Number.NaN },
    { name: "a Number object", value: { score: new Number(Number.NaN) }, pointer: "/score", number: Number.NaN },
    { name: "a toJSON result", value: { at: { toJSON: () => Number.POSITIVE_INFINITY } }, pointer: "/at", number: Number.POSITIVE_INFINITY },
    { name: "the payload itself", value: Number.NaN, pointer: "", number: Number.NaN },
  ])("refuses $name, naming its pointer", ({ value, pointer, number }) => {
    let caught: unknown;
    try {
      encodeJSON(value, "filter");
    } catch (err) {
      caught = err;
    }
    expect(caught).toBeInstanceOf(NonFiniteNumberError);
    expect(caught).toBeInstanceOf(RangeError);
    expect(caught).toMatchObject({ name: "NonFiniteNumberError", payload: "filter", pointer, value: number });
  });

  it("says which member, in which payload", () => {
    expect(() => encodeJSON({ prefs: { score: Number.POSITIVE_INFINITY } }, "patch"))
      .toThrow("the patch member /prefs/score is Infinity, which JSON cannot carry: JSON.stringify would send null");
  });
});
