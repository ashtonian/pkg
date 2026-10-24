import { describe, expect, it } from "vitest";
import { SchemaValidationError } from "./core/errors.js";
import { filter } from "./filter/builder.js";
import type { FilterFieldSchema, FilterSchema } from "./filter/types.js";
import { validateFilter, validateSort } from "./filter/validate.js";
import { validatePatch } from "./patch/validate.js";
import { SCHEMA_VERSION } from "./schema.js";
import { SchemaValidator } from "./validate.js";

const stringField: FilterFieldSchema = { name: "name", data_type: "string", operators: ["eq"] };

function expectCode(fn: () => void, code: string): void {
  let error: unknown;
  try { fn(); } catch (caught) { error = caught; }
  expect(error).toBeInstanceOf(SchemaValidationError);
  expect(error).toMatchObject({ code });
}

describe("schema maps have own fields only, like Go maps", () => {
  for (const name of ["__proto__", "constructor", "toString", "hasOwnProperty"]) {
    it(`rejects undeclared ${name} with the server's codes`, () => {
      const fields = {};
      expectCode(() => { validateFilter({ [name]: "value" }, { fields }); }, "E2071");
      expectCode(() => { validateSort(name, { fields, max_fields: 3 }); }, "E2079");
      const validator = new SchemaValidator({ version: SCHEMA_VERSION, resource: "items", filter: { fields } });
      expectCode(() => { validator.validateInclude([name]); }, "E2071");
      expect(validator.operatorsFor(name)).toEqual([]);
      expect(validator.enumValuesFor(name)).toEqual([]);
      expect(validator.fieldMeta(name)).toBeUndefined();
    });

    it(`accepts explicitly declared ${name}`, () => {
      const schema: FilterSchema = { fields: { [name]: { ...stringField, name, includable: true } } };
      expect(() => { validateFilter({ [name]: "value" }, schema); }).not.toThrow();
      expect(() => { validateSort(name, { fields: { [name]: {} }, max_fields: 3 }); }).not.toThrow();
      const validator = new SchemaValidator({ version: SCHEMA_VERSION, resource: "items", filter: schema });
      expect(() => { validator.validateInclude([name]); }).not.toThrow();
      expect(validator.operatorsFor(name)).toEqual(["eq"]);
      expect(validator.filterableFields()).toEqual([name]);
    });
  }

  it("does not accept inherited patch paths", () => {
    const paths: Record<string, { data_type: "string"; allowed_ops: ["replace"] }> = {};
    Object.setPrototypeOf(paths, { "/name": { data_type: "string", allowed_ops: ["replace"] } });
    expectCode(() => { validatePatch({ name: "value" }, { paths, max_operations: 10 }); }, "E2076");
  });
});

describe("sort validation matches Go ParseSort bounds", () => {
  const fields = { name: {} };
  it("counts empty comma slots before skipping them", () => {
    expectCode(() => { validateSort("name,,,", { fields, max_fields: 3 }); }, "E2081");
    expect(() => { validateSort("name,,", { fields, max_fields: 3 }); }).not.toThrow();
  });
  it("uses the server default of three fields when max_fields is zero", () => {
    expectCode(() => { validateSort("name,name,name,name", { fields, max_fields: 0 }); }, "E2081");
    expect(() => { validateSort("name,name,name", { fields, max_fields: 0 }); }).not.toThrow();
  });
  it("preserves signs and whitespace handling", () => {
    expect(() => { validateSort(" +name, -name, ", { fields, max_fields: 3 }); }).not.toThrow();
    expectCode(() => { validateSort("+missing", { fields, max_fields: 3 }); }, "E2079");
  });
});

describe("schema introspection", () => {
  it("returns decoded JSON field names that can be passed back to merge-patch validation", () => {
    const validator = new SchemaValidator({ version: SCHEMA_VERSION, resource: "items", patch: {
      max_operations: 10,
      paths: {
        "/unit~1si~0v": { data_type: "string", allowed_ops: ["replace"] },
        "/literal~01": { data_type: "string", allowed_ops: ["replace"] },
        "/": { data_type: "string", allowed_ops: ["replace"] },
      },
    } });
    expect(validator.patchableFields()).toEqual(["unit/si~v", "literal~1", ""]);
    for (const name of validator.patchableFields()) {
      expect(() => { validator.validatePatch({ [name]: "value" }); }).not.toThrow();
    }
  });
});

describe("filter builder", () => {
  it("preserves a field called __proto__ on the JSON wire", () => {
    const result = filter<Record<string, unknown>>().where("__proto__", { $eq: "value" }).build();
    expect(JSON.stringify(result)).toBe('{"__proto__":{"$eq":"value"}}');
    expect(Object.getPrototypeOf(result)).toBe(Object.prototype);
  });
  it("does not add later logical branches to an already built filter", () => {
    const builder = filter<{ name: string }>().or({ name: "one" }).and({ name: "one" });
    const first = builder.build();
    builder.or({ name: "two" }).and({ name: "two" });
    expect(first).toEqual({ $or: [{ name: "one" }], $and: [{ name: "one" }] });
    expect(builder.build().$or).toHaveLength(2);
    expect(builder.build().$and).toHaveLength(2);
  });
});
