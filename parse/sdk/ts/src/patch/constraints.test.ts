import { describe, expect, it } from "vitest";
import { SchemaValidationError, SchemaValidationErrors } from "../core/errors.js";
import type { ConstraintFailure } from "../core/errors.js";
import type { PatchSchema } from "./types.js";
import { validatePatch } from "./validate.js";

const schema: PatchSchema = {
  max_operations: 50,
  paths: {
    "/mode": { data_type: "string", allowed_ops: ["replace"], enum_values: ["on", "off"] },
    "/title": { data_type: "string", allowed_ops: ["replace"], max_length: 3 },
    "/values": { data_type: "array", elem: "string", allowed_ops: ["replace"], enum_values: ["a", "b"], max_items: 2 },
  },
};

function caught(input: Record<string, unknown>, schema: PatchSchema): SchemaValidationError {
  let error: unknown;
  try { validatePatch(input, schema); } catch (caught) { error = caught; }
  expect(error).toBeInstanceOf(SchemaValidationError);
  if (!(error instanceof SchemaValidationError)) throw new Error("missing schema failure");
  return error;
}

describe("declared patch constraints", () => {
  it("reports every invalid value and retains the first field and code", () => {
    const error = caught({ mode: "invalid", title: "long", values: ["bad", "worse", "a"] }, schema);
    expect(error).toMatchObject({ field: "mode", code: "E2077", constraint: "enum", limit: "on,off" });
    expect(error).toBeInstanceOf(SchemaValidationErrors);
    if (!(error instanceof SchemaValidationErrors)) throw new Error("missing aggregate failure");
    expect(error.errors.map(({ field, constraint, limit }) => ({ field, constraint, limit }))).toEqual([
      { field: "mode", constraint: "enum", limit: "on,off" },
      { field: "title", constraint: "maxlen", limit: "3" },
      { field: "values", constraint: "enum", limit: "a,b" },
      { field: "values", constraint: "enum", limit: "a,b" },
      { field: "values", constraint: "maxitems", limit: "2" },
    ]);
  });

  for (const { path, value, expected } of [
    { path: { data_type: "string", allowed_ops: ["replace"], min_length: 3 }, value: "é", expected: { constraint: "minlen", limit: "3" } },
    { path: { data_type: "int", allowed_ops: ["replace"], minimum: "2" }, value: 1, expected: { constraint: "min", limit: "2" } },
    { path: { data_type: "decimal", allowed_ops: ["replace"], maximum: "999.99" }, value: "1000", expected: { constraint: "max", limit: "999.99" } },
    { path: { data_type: "array", elem: "string", allowed_ops: ["replace"], min_items: 1 }, value: [], expected: { constraint: "minitems", limit: "1" } },
  ] satisfies { path: PatchSchema["paths"][string]; value: unknown; expected: ConstraintFailure }[]) {
    it(`names ${expected.constraint} with the schema limit`, () => {
      expect(caught({ value }, { paths: { "/value": path }, max_operations: 50 })).toMatchObject(expected);
    });
  }

  it("does not invent a declared constraint for a type mismatch", () => {
    const error = caught({ title: 12 }, schema);
    expect(error).toMatchObject({ field: "title", code: "E2077" });
    expect(error.constraint).toBeUndefined();
    expect(error.limit).toBeUndefined();
  });
});
