import { describe, expect, it } from "vitest";
import { SchemaValidationError } from "./core/errors.js";
import { and, filter, not, or } from "./filter/builder.js";
import type { FilterExpression, FilterFieldSchema, FilterSchema } from "./filter/types.js";
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

describe("time, interval and network values are text the server parses", () => {
  for (const type of ["time", "interval", "inet", "cidr", "macaddr"] as const) {
    it(`checks a ${type} value is a string`, () => {
      const schema: FilterSchema = { fields: { v: { name: "v", data_type: type, operators: ["eq", "in"] } } };
      expect(() => { validateFilter({ v: "text" }, schema); }).not.toThrow();
      expect(() => { validateFilter({ v: { $in: ["a", "b"] } }, schema); }).not.toThrow();
      expectCode(() => { validateFilter({ v: 42 }, schema); }, "E2073");
      const paths = { "/v": { data_type: type, allowed_ops: ["replace" as const] } };
      expect(() => { validatePatch({ v: "text" }, { paths, max_operations: 10 }); }).not.toThrow();
      expectCode(() => { validatePatch({ v: 42 }, { paths, max_operations: 10 }); }, "E2077");
    });
  }
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
  it("takes a scalar array's element conditions, typed by the element", () => {
    interface Device { tags: string[]; sizes: number[] }
    const result = filter<Device>()
      .where("tags", { $some: { $or: [{ $like: "a%" }, { $eq: "b" }] } })
      .where("sizes", { $none: { $gte: 1, $lt: 5 } })
      .build();
    expect(result).toEqual({ tags: { $some: { $or: [{ $like: "a%" }, { $eq: "b" }] } }, sizes: { $none: { $gte: 1, $lt: 5 } } });
    // @ts-expect-error a string element compares with strings
    filter<Device>().where("tags", { $some: { $eq: 3 } });
  });
  it("merges a second condition on a field and refuses one that would drop the first", () => {
    interface P { price: number; name: string }
    expect(filter<P>().where("price", { $gte: 1 }).where("price", { $lte: 5 }).build()).toEqual({ price: { $gte: 1, $lte: 5 } });
    expect(() => filter<P>().where("price", { $gte: 1 }).where("price", { $gte: 2 })).toThrow(RangeError);
    expect(() => filter<P>().where("name", "a").where("name", { $ne: "b" })).toThrow(RangeError);
    expect(() => filter<P>().where("name", { $ne: "b" }).where("name", "a")).toThrow(RangeError);
  });
  it("is immutable: where returns a new builder", () => {
    const base = filter<{ name: string; price: number }>().where("name", "a");
    const narrowed = base.where("price", { $lt: 5 });
    expect(base.build()).toEqual({ name: "a" });
    expect(narrowed.build()).toEqual({ name: "a", price: { $lt: 5 } });
    const built = narrowed.build();
    (built as Record<string, unknown>)["name"] = "changed";
    expect(narrowed.build()).toEqual({ name: "a", price: { $lt: 5 } });
  });
  it("copies operands when added and the expression when built", () => {
    interface Row { name: string; labels: Record<string, unknown>; seen: Date }
    const names = ["a"];
    const labels = { env: { tier: "prod" } };
    const seen = new Date(0);
    const b = filter<Row>().where("name", { $in: names }).where("labels", { $contains: labels }).where("seen", { $gte: seen });
    names.push("b");
    labels.env.tier = "dev";
    seen.setTime(1);
    const want = { name: { $in: ["a"] }, labels: { $contains: { env: { tier: "prod" } } }, seen: { $gte: new Date(0) } };
    expect(b.build()).toEqual(want);
    const built = b.build() as unknown as { name: { $in: string[] }; seen: { $gte: Date } };
    built.name.$in.push("c");
    built.seen.$gte.setTime(2);
    expect(b.build()).toEqual(want);
  });
  it("keeps an operand that is no plain data as the caller's value", () => {
    class Amount {
      constructor(readonly text: string) {}
      toJSON(): string { return this.text; }
    }
    const amount = new Amount("1.50");
    const built = filter<{ price: Amount }>().where("price", { $gte: amount }).build() as { price: { $gte: Amount } };
    expect(built.price.$gte).toBe(amount);
    expect(JSON.stringify(built)).toBe('{"price":{"$gte":"1.50"}}');
  });
  it("has no logical methods: and, or and not are combinators", () => {
    const b = filter<{ name: string }>() as unknown as Record<string, unknown>;
    expect(b["or"]).toBeUndefined();
    expect(b["and"]).toBeUndefined();
    expect(b["not"]).toBeUndefined();
  });
});

describe("filter combinators", () => {
  interface P { name: string; price: number }
  const a = { name: "a" }, b = { name: "b" }, c = { price: 3 };
  it("or disjoins, and conjoins, not negates", () => {
    expect(or<P>(a, b)).toEqual({ $or: [a, b] });
    expect(and<P>(a, c)).toEqual({ $and: [a, c] });
    expect(not<P>(a)).toEqual({ $not: a });
    expect(or<P>(filter<P>().where("name", "a"), b)).toEqual({ $or: [a, b] });
  });
  it("flattens nested groups of the same kind and keeps mixed ones", () => {
    expect(or<P>(a, or<P>(b, c))).toEqual({ $or: [a, b, c] });
    expect(and<P>(and<P>(a, b), c)).toEqual({ $and: [a, b, c] });
    expect(or<P>(and<P>(a, b), c)).toEqual({ $or: [{ $and: [a, b] }, c] });
    let loop: FilterExpression<P> = {};
    for (const f of [a, b, c]) loop = and<P>(loop, f);
    expect(loop).toEqual({ $and: [a, b, c] });
  });
  it("treats the empty filter as every row", () => {
    expect(and<P>()).toEqual({});
    expect(and<P>({}, a, {})).toEqual(a);
    expect(or<P>(a)).toEqual(a);
    expect(or<P>(a, {})).toEqual({});
  });
  it("throws for what would match no row", () => {
    expect(() => or<P>()).toThrow(RangeError);
    expect(() => not<P>({})).toThrow(RangeError);
  });
  it("is the server's disjunction on the wire", () => {
    // The old builder's .or() set $or beside the field conditions, which
    // the server conjoins.
    expect(JSON.stringify(or<P>(filter<P>().where("name", "a"), filter<P>().where("name", "b")))).toBe('{"$or":[{"name":"a"},{"name":"b"}]}');
  });
});

describe("patch path patterns", () => {
  const replace = { data_type: "string" as const, allowed_ops: ["replace" as const] };
  it("resolves a member through the most specific matching pattern, as the server does", () => {
    const schema = { max_operations: 10, paths: { "/*": { data_type: "int" as const, allowed_ops: ["replace" as const] }, "/name": replace } };
    expect(() => { validatePatch({ name: "a" }, schema); }).not.toThrow();
    expect(() => { validatePatch({ count: 1 }, schema); }).not.toThrow();
    expectCode(() => { validatePatch({ count: "x" }, schema); }, "E2077");
    expect(() => { validatePatch({ any: "x" }, { max_operations: 10, paths: { "**": replace } }); }).not.toThrow();
    expectCode(() => { validatePatch({ any: "x" }, { max_operations: 10, paths: { "/a/*": replace } }); }, "E2076");
  });
});

describe("relation key limit", () => {
  const members = { data_type: "array" as const, elem: "string" as const, relation: true, nullable: true, allowed_ops: ["replace" as const] };
  const quoted = { ...members, elem: "int" as const, number_kind: "int64", wire: "string" as const };
  it("refuses more distinct keys than max_relation_values, as the server does (E2119)", () => {
    const schema = { max_operations: 10, max_relation_values: 2, paths: { "/members": members, "/ids": quoted } };
    expect(() => { validatePatch({ members: ["a", "b", "a", "b"] }, schema); }).not.toThrow();
    expectCode(() => { validatePatch({ members: ["a", "b", "c"] }, schema); }, "E2119");
    expect(() => { validatePatch({ ids: ["5", "5", "9007199254740993"] }, schema); }).not.toThrow();
    expectCode(() => { validatePatch({ ids: ["1", "2", "3"] }, schema); }, "E2119");
    expect(() => { validatePatch({ members: ["a", "b", "c"] }, { ...schema, max_relation_values: 0 }); }).not.toThrow();
  });
});

describe("relation keys of several columns", () => {
  const stock = {
    data_type: "array" as const, relation: true, nullable: true, allowed_ops: ["replace" as const],
    key: [{ data_type: "int" as const, number_kind: "int64" }, { data_type: "string" as const }],
  };
  const schema = { max_operations: 10, max_relation_values: 2, paths: { "/stock": stock } };
  it("takes each key as an array of a value per column, as the server does", () => {
    expect(() => { validatePatch({ stock: [[1, "a"], [2, "b"]] }, schema); }).not.toThrow();
    expectCode(() => { validatePatch({ stock: [[1]] }, schema); }, "E2077");
    expectCode(() => { validatePatch({ stock: [[1, "a", "x"]] }, schema); }, "E2077");
    expectCode(() => { validatePatch({ stock: ["1:a"] }, schema); }, "E2077");
    expectCode(() => { validatePatch({ stock: [["one", "a"]] }, schema); }, "E2077");
    expectCode(() => { validatePatch({ stock: [null] }, schema); }, "E2118");
  });
  it("counts a repeated key once against max_relation_values (E2119)", () => {
    expect(() => { validatePatch({ stock: [[1, "a"], [1, "a"], [2, "b"]] }, schema); }).not.toThrow();
    expectCode(() => { validatePatch({ stock: [[1, "a"], [1, "b"], [2, "b"]] }, schema); }, "E2119");
  });
});
