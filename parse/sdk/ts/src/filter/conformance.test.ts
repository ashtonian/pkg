// Go⇔TS filter-grammar conformance.
//
// The corpus at public-pkg/parse/testdata/filter_conformance.json is generated
// from the authoritative Go parser. This test asserts the TS validator agrees
// on validity + error code for every case, so the two grammar implementations
// can't silently drift. Regenerate the corpus with:
//
//   go test -tags=unit -run TestEmitFilterConformance -update ./public-pkg/parse/
//
// The TS client has no parser, so it does not reconstruct the canonical AST or
// SQL (those are Go-side checks in filter_conformance_test.go). What the client
// owns — and what this test guards — is the validate-before-send contract.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { SchemaValidationError } from "../core/errors.js";
import type { FilterSchema } from "./types.js";
import { validateFilter } from "./validate.js";

interface ConformanceCase {
  name: string;
  input: Record<string, unknown>;
  valid: boolean;
  errorCode?: string;
  tsApplicable?: boolean;
}

const corpus = JSON.parse(
  readFileSync(
    new URL("../../../../testdata/filter_conformance.json", import.meta.url),
    "utf8",
  ),
) as ConformanceCase[];

// confSchema mirrors the Go confEntity struct the corpus is generated against.
// Keep the field/operator/enum sets in lockstep with filter_conformance_test.go.
const confSchema: FilterSchema = {
  fields: {
    id: { name: "id", data_type: "string", operators: ["eq", "ne", "in", "nin"] },
    name: {
      name: "name", data_type: "string",
      operators: ["eq", "ne", "like", "ilike", "contains", "null", "not_null", "exists"],
    },
    status: {
      name: "status", data_type: "string",
      operators: ["eq", "ne", "in", "nin"],
      enum_values: ["active", "offline", "error"],
    },
    count: {
      name: "count", data_type: "int",
      operators: ["eq", "ne", "gt", "gte", "lt", "lte", "in", "nin"],
    },
    price: {
      name: "price", data_type: "decimal",
      operators: ["eq", "ne", "gt", "gte", "lt", "lte", "in", "nin"],
    },
    active: { name: "active", data_type: "bool", operators: ["eq", "ne"] },
    created: { name: "created", data_type: "timestamp", operators: ["eq", "gt", "gte", "lt", "lte"] },
    meta: { name: "meta", data_type: "json", operators: ["eq", "ne", "contains", "has_key", "exists", "null"] },
    "meta.region": {
      name: "meta.region", data_type: "string",
      operators: ["eq", "ne", "contains", "like", "ilike", "in", "nin", "null", "not_null", "exists"],
    },
    note: { name: "note", data_type: "string", operators: ["not_null"] },
    lines: {
      name: "lines", data_type: "json",
      operators: ["eq", "ne", "contains", "has_key", "exists", "null", "some", "none"],
      elements: {
        sku: { name: "sku", data_type: "string", operators: ["eq", "in"] },
        qty: { name: "qty", data_type: "int", operators: ["eq", "gt", "lt"] },
      },
    },
    parts: {
      name: "parts", data_type: "string",
      operators: ["eq", "ne", "in", "nin", "null", "not_null", "some", "none"],
      relation: { kind: "has_many", target: "parts" },
    },
  },
};

// The target of the declared relation "parts" (Go confPart).
const partsSchema: FilterSchema = {
  fields: {
    id: { name: "id", data_type: "string", operators: ["eq", "in"] },
    state: { name: "state", data_type: "string", operators: ["eq", "ne"], enum_values: ["ok", "worn"] },
  },
};
const related = (name: string): FilterSchema | undefined => (name === "parts" ? partsSchema : undefined);

describe("filter conformance corpus", () => {
  it("is non-empty", () => {
    expect(corpus.length).toBeGreaterThan(0);
  });

  for (const c of corpus) {
    if (c.tsApplicable === false) continue;

    it(c.name, () => {
      let thrown: unknown;
      try {
        validateFilter(c.input, confSchema, related);
      } catch (e) {
        thrown = e;
      }

      if (c.valid) {
        expect(thrown, `${c.name} should validate but threw ${String(thrown)}`).toBeUndefined();
        return;
      }

      expect(thrown, `${c.name} should be rejected`).toBeInstanceOf(SchemaValidationError);
      expect((thrown as SchemaValidationError).code).toBe(c.errorCode);
    });
  }
});
