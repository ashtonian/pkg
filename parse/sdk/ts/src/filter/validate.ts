import { SchemaValidationError } from "../core/errors.js";
import type { FilterFieldSchema, FilterOperator, FilterSchema, SortSchema } from "./types.js";

// Mirrors the Go filter parser (public-pkg/parse/filter): same structure,
// same E-codes. The conformance corpus (testdata/filter_conformance.json)
// holds the two to one answer per input. Checks the server bounds by
// deployment config (depth, cost, sizes) and value formats the client cannot
// judge exactly (timestamps, UUID spellings) are left to the server.

const OPERATOR_KEYS = new Set([
  "$eq", "$ne", "$gt", "$gte", "$lt", "$lte", "$in", "$nin", "$like", "$ilike",
  "$null", "$not_null", "$exists", "$contains", "$has_key", "$near", "$within", "$intersects",
  "$some", "$none", "$is", "$is_not",
  "$or", "$and", "$not",
]);

const NESTED_OPS = new Set<FilterOperator>(["some", "none", "is", "is_not"]);

/**
 * Looks up a relation target's filter schema by resource name. Without one
 * (or when it returns undefined), a relation's nested filter is left to the
 * server.
 */
export type RelatedSchemas = (resource: string) => FilterSchema | undefined;

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Validate a filter expression against a FilterSchema. */
export function validateFilter(
  filter: Record<string, unknown>,
  schema: FilterSchema,
  related?: RelatedSchemas,
): void {
  for (const key of Object.keys(filter).toSorted()) {
    const value = filter[key];
    if (key === "$and" || key === "$or" || key === "$not") {
      validateLogical(key, value, schema, related);
      continue;
    }
    const field = Object.hasOwn(schema.fields, key) ? schema.fields[key] : undefined;
    if (!field) {
      throw new SchemaValidationError(
        key, "E2071",
        `Unknown filter field "${key}". Valid fields: ${Object.keys(schema.fields).join(", ")}`,
      );
    }
    // An object with operator keys is an operator object; any other value —
    // including a plain object, which is a JSON document — is equality.
    if (isObject(value) && Object.keys(value).some((k) => OPERATOR_KEYS.has(k))) {
      validateOperators(key, value, field, related);
      continue;
    }
    assertAllowed(key, "eq", field);
    validateValue(key, "eq", value, field);
  }
}

function validateLogical(key: "$and" | "$or" | "$not", value: unknown, schema: FilterSchema, related?: RelatedSchemas): void {
  if (key === "$not") {
    if (!isObject(value)) {
      throw new SchemaValidationError(key, "E2086", "$not requires an object value");
    }
    if (Object.keys(value).length === 0) {
      throw new SchemaValidationError(key, "E2086", "$not requires a non-empty object");
    }
    validateFilter(value, schema, related);
    return;
  }
  if (!Array.isArray(value)) {
    throw new SchemaValidationError(key, "E2086", `${key} requires an array value`);
  }
  let conditions = 0;
  for (const branch of value) {
    if (!isObject(branch)) {
      throw new SchemaValidationError(key, "E2086", `${key} array items must be objects`);
    }
    validateFilter(branch, schema, related);
    conditions += Object.keys(branch).length;
  }
  if (conditions === 0) {
    throw new SchemaValidationError(key, "E2086", `${key} requires at least one non-empty condition`);
  }
}

function validateOperators(name: string, ops: Record<string, unknown>, field: FilterFieldSchema, related?: RelatedSchemas): void {
  for (const key of Object.keys(ops).toSorted()) {
    if (!key.startsWith("$")) {
      throw new SchemaValidationError(
        name, "E2072",
        `Unexpected key "${key}" in operator object (keys must start with $)`,
      );
    }
    if (!OPERATOR_KEYS.has(key)) {
      throw new SchemaValidationError(name, "E2072", `Unknown operator "${key}"`);
    }
    const op = key.slice(1) as FilterOperator;
    assertAllowed(name, op, field);
    const val = ops[key];
    if (NESTED_OPS.has(op)) {
      validateNested(name, op, val, field, related);
      continue;
    }
    if (isNullTest(op)) {
      if (typeof val !== "boolean") {
        throw new SchemaValidationError(name, "E2073", `$${op} requires a boolean value`);
      }
      continue;
    }
    validateValue(name, op, val, field);
  }
}

// validateNested checks a relation's or a JSON array's nested filter: an
// object, valid against the element paths or the target's schema when the
// client has it (the server checks it otherwise).
function validateNested(name: string, op: FilterOperator, val: unknown, field: FilterFieldSchema, related?: RelatedSchemas): void {
  if (!isObject(val)) {
    throw new SchemaValidationError(name, "E2086", `$${op} on "${name}" requires a filter object`);
  }
  const nested = field.elements
    ? { fields: field.elements }
    : field.relation?.target !== undefined ? related?.(field.relation.target) : undefined;
  if (nested) validateFilter(val, nested, related);
}

/** Relation targets whose schemas a filter's nested relation filters need, beyond those known. */
export function relatedTargets(
  filter: Record<string, unknown>,
  schema: FilterSchema,
  related?: RelatedSchemas,
): string[] {
  const out = new Set<string>();
  const walk = (f: Record<string, unknown>, s: FilterSchema): void => {
    for (const [key, value] of Object.entries(f)) {
      if (key === "$and" || key === "$or") {
        if (Array.isArray(value)) for (const b of value) if (isObject(b)) walk(b, s);
        continue;
      }
      if (key === "$not") {
        if (isObject(value)) walk(value, s);
        continue;
      }
      const field = Object.hasOwn(s.fields, key) ? s.fields[key] : undefined;
      const target = field?.relation?.target;
      if (target === undefined || !isObject(value)) continue;
      for (const [op, nested] of Object.entries(value)) {
        if (!NESTED_OPS.has(op.slice(1) as FilterOperator) || !isObject(nested)) continue;
        const ts = related?.(target);
        if (ts) walk(nested, ts);
        else out.add(target);
      }
    }
  };
  walk(filter, schema);
  return [...out];
}

// The NULL tests are one capability: a field that declares any of them
// allows all three, as the Go parser does.
function isNullTest(op: FilterOperator): boolean {
  return op === "null" || op === "not_null" || op === "exists";
}

function assertAllowed(name: string, op: FilterOperator, field: FilterFieldSchema): void {
  const allowed = isNullTest(op) ? field.operators.some(isNullTest) : field.operators.includes(op);
  if (!allowed) {
    throw new SchemaValidationError(
      name, "E2072",
      `Operator "${op}" is not allowed on field "${name}". Allowed: ${field.operators.join(", ")}`,
    );
  }
}

function validateValue(name: string, op: FilterOperator, val: unknown, field: FilterFieldSchema): void {
  if (op === "has_key") {
    if (typeof val !== "string") {
      throw new SchemaValidationError(name, "E2073", `$has_key on "${name}" requires a string key`);
    }
    return;
  }
  if (field.data_type === "json" || field.data_type === "object") {
    if (val === null) {
      throw new SchemaValidationError(name, "E2073", "null is not a comparable document; use $null");
    }
    return;
  }
  if (field.data_type === "array" || field.data_type === "geo") {
    return; // element types and shapes are the server's to check
  }
  switch (op) {
    case "contains":
      if (typeof val !== "string") {
        throw new SchemaValidationError(name, "E2073", `$contains on "${name}" requires a string`);
      }
      return;
    case "like":
    case "ilike":
      if (typeof val !== "string") {
        throw new SchemaValidationError(name, "E2073", `$${op} on "${name}" requires a string pattern`);
      }
      if (endsWithEscape(val)) {
        throw new SchemaValidationError(name, "E2073", "pattern ends with the escape character \\");
      }
      return;
    case "in":
    case "nin":
      if (!Array.isArray(val)) {
        throw new SchemaValidationError(name, "E2062", `$${op} on "${name}" requires an array value`);
      }
      for (const member of val) validateMember(name, member, field);
      return;
    default:
      validateMember(name, val, field);
  }
}

// endsWithEscape reports an unpaired trailing backslash, which the server
// rejects rather than guessing a literal.
function endsWithEscape(pattern: string): boolean {
  let run = 0;
  for (let i = pattern.length - 1; i >= 0 && pattern[i] === "\\"; i--) run++;
  return run % 2 === 1;
}

// validateMember checks one scalar against the field type, then the enum
// allowlist — the Go normalizeMember order.
function validateMember(name: string, val: unknown, field: FilterFieldSchema): void {
  const type = field.data_type;
  let ok = true;
  switch (type) {
    case "string":
    case "timestamp":
    case "uuid":
      ok = typeof val === "string";
      break;
    case "int":
      ok = typeof val === "number" && Number.isInteger(val);
      break;
    case "float":
      ok = typeof val === "number" && Number.isFinite(val);
      break;
    case "bool":
      ok = typeof val === "boolean";
      break;
  }
  if (!ok) {
    const code = type === "uuid" ? "E2116" : "E2073";
    throw new SchemaValidationError(name, code, `Field "${name}" expects ${type}, got ${val === null ? "null" : typeof val}`);
  }
  if (field.enum_values && field.enum_values.length > 0 && typeof val === "string" && !field.enum_values.includes(val)) {
    throw new SchemaValidationError(
      name, "E2087",
      `Value "${val}" is not a valid enum value for "${name}". Allowed: ${field.enum_values.join(", ")}`,
    );
  }
}

/** Validate a sort string against a SortSchema. */
export function validateSort(sortStr: string, schema: SortSchema): void {
  if (!sortStr) return;
  // Go counts comma-separated slots before ignoring empty entries.
  const fields = sortStr.split(",");
  const maxFields = schema.max_fields === 0 ? 3 : schema.max_fields;
  if (fields.length > maxFields) {
    throw new SchemaValidationError(
      "*", "E2081",
      `Too many sort fields (${String(fields.length)}). Maximum: ${String(maxFields)}`,
    );
  }
  for (const part of fields) {
    const raw = part.trim();
    if (!raw) continue;
    const field = raw.startsWith("-") || raw.startsWith("+") ? raw.slice(1) : raw;
    if (!Object.hasOwn(schema.fields, field) || !schema.fields[field]) {
      throw new SchemaValidationError(
        field, "E2079",
        `Sort field "${field}" is not allowed. Sortable fields: ${Object.keys(schema.fields).join(", ")}`,
      );
    }
  }
}
