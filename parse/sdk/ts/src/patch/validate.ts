import { isCivilDate } from "../core/date.js";
import { compareDecimals, isDecimal } from "../core/decimal.js";
import { SchemaValidationError, SchemaValidationErrors } from "../core/errors.js";
import type { ConstraintFailure } from "../core/errors.js";
import type { FieldType } from "../core/types.js";
import type { PatchOp, PatchPathSchema, PatchSchema } from "./types.js";

/**
 * Validate a JSON Merge Patch against a PatchSchema as the Go parser does:
 * each key is a patchable top-level path (an RFC 6901 JSON Pointer), null
 * only where the path is nullable (and `remove` allowed), every other value
 * of the path's type — numbers integral and within the path's Go kind,
 * strings in its enum, values within its bounds, arrays of its element type
 * within its element counts (each element in the enum and bounds), a
 * relation's membership a list —
 * and the operation count within the maximum, counting each
 * top-level member and each nested object below it. The corpus
 * testdata/patch_conformance.json holds the two to one answer per input.
 * Formats the client cannot judge exactly (timestamp and UUID spellings),
 * values inside JSON documents, and the server's depth and size bounds are
 * left to the server.
 */
export function validatePatch(input: Record<string, unknown>, schema: PatchSchema): void {
  const fields = Object.keys(input);

  const ops = mergeOperations(input, 1);
  if (schema.max_operations > 0 && ops > schema.max_operations) {
    throw new SchemaValidationError(
      "*", "E2066",
      `Too many patch operations (${String(ops)}). Maximum: ${String(schema.max_operations)}`,
    );
  }

  const failures = fields.toSorted().flatMap(field => validateField(field, input[field], schema));
  const first = failures[0];
  if (failures.length === 1 && first) throw first;
  if (failures.length > 1) throw new SchemaValidationErrors(failures);
}

function validateField(field: string, value: unknown, schema: PatchSchema): SchemaValidationError[] {
  const path = `/${escapePointerToken(field)}`;
  const pathSchema = Object.hasOwn(schema.paths, path) ? schema.paths[path] : undefined;
  if (!pathSchema) {
    return [new SchemaValidationError(field, "E2076",
      `Field "${field}" is not patchable. Patchable fields: ${Object.keys(schema.paths).map(unescapePointer).join(", ")}`)];
  }
  if (value === null && !pathSchema.nullable && !pathSchema.relation) {
    const constraint: ConstraintFailure | undefined = pathSchema.min_items !== undefined && pathSchema.min_items > 0
      ? { constraint: "minitems", limit: String(pathSchema.min_items) }
      : undefined;
    return [new SchemaValidationError(field, "E2077", `Field "${field}" cannot be null`, constraint)];
  }
  const op: PatchOp = value === null ? "remove" : "replace";
  if (!pathSchema.allowed_ops.includes(op)) {
    return [new SchemaValidationError(field, "E2064",
      `Operation "${op}" is not allowed on "${field}". Allowed: ${pathSchema.allowed_ops.join(", ")}`)];
  }
  return value === null ? [] : validatePatchValue(field, value, pathSchema);
}

function validatePatchValue(field: string, value: unknown, path: PatchPathSchema): SchemaValidationError[] {
  if (path.relation) {
    return Array.isArray(value) ? [] : [new SchemaValidationError(field, "E2118", `Relation "${field}" takes a list of keys`)];
  }
  if (path.data_type === "array") {
    if (!Array.isArray(value)) {
      return [new SchemaValidationError(field, "E2077", `Field "${field}" expects an array`)];
    }
    const elementType = path.elem;
    if (elementType && value.some(el => !scalarOK(el, elementType, path.number_kind))) {
      return [new SchemaValidationError(field, "E2077", `Field "${field}" expects ${elementType} elements`)];
    }
    const failures = elementType ? value.flatMap(el => checkValueTags(field, el, elementType, path)) : [];
    if (path.min_items !== undefined && value.length < path.min_items) {
      failures.push(new SchemaValidationError(field, "E2077", `Field "${field}" must hold at least ${String(path.min_items)} elements, got ${String(value.length)}`,
        { constraint: "minitems", limit: String(path.min_items) }));
    }
    if (path.max_items !== undefined && value.length > path.max_items) {
      failures.push(new SchemaValidationError(field, "E2077", `Field "${field}" must hold at most ${String(path.max_items)} elements, got ${String(value.length)}`,
        { constraint: "maxitems", limit: String(path.max_items) }));
    }
    return failures;
  }
  if (!scalarOK(value, path.data_type, path.number_kind)) {
    return [new SchemaValidationError(field, "E2077", `Field "${field}" expects ${path.data_type}, got ${typeof value}`)];
  }
  return checkValueTags(field, value, path.data_type, path);
}

/** The path's enum and bounds, on a value of type (an array path's element type). */
function checkValueTags(field: string, value: unknown, type: FieldType, path: PatchPathSchema): SchemaValidationError[] {
  const failures: SchemaValidationError[] = [];
  if (path.enum_values && path.enum_values.length > 0 && typeof value === "string" && !path.enum_values.includes(value)) {
    failures.push(new SchemaValidationError(field, "E2077", `Value "${value}" is not allowed for "${field}". Allowed: ${path.enum_values.join(", ")}`,
      { constraint: "enum", limit: path.enum_values.join(",") }));
  }
  if (type === "string" && typeof value === "string") {
    // Count Unicode code points, matching Go's string bounds.
    const length = Array.from(value).length;
    if (path.min_length !== undefined && length < path.min_length) {
      failures.push(new SchemaValidationError(field, "E2077", `Field "${field}" must hold at least ${String(path.min_length)} characters, got ${String(length)}`,
        { constraint: "minlen", limit: String(path.min_length) }));
    }
    if (path.max_length !== undefined && length > path.max_length) {
      failures.push(new SchemaValidationError(field, "E2077", `Field "${field}" must hold at most ${String(path.max_length)} characters, got ${String(length)}`,
        { constraint: "maxlen", limit: String(path.max_length) }));
    }
    return failures;
  }
  if (path.minimum !== undefined && compareBound(value, path.minimum, type, path.number_kind) < 0) {
    failures.push(new SchemaValidationError(field, "E2077", `Field "${field}" must be at least ${path.minimum}`,
      { constraint: "min", limit: path.minimum }));
  }
  if (path.maximum !== undefined && compareBound(value, path.maximum, type, path.number_kind) > 0) {
    failures.push(new SchemaValidationError(field, "E2077", `Field "${field}" must be at most ${path.maximum}`,
      { constraint: "max", limit: path.maximum }));
  }
  return failures;
}

// compareBound orders a number against a bound as the server does: a float
// at its kind's precision (both rounded alike, so max "0.1" admits a
// float32 0.1), an integer or decimal exactly. Anything else is not the
// bound's to judge (0).
function compareBound(value: unknown, bound: string, type: FieldType, kind?: string): number {
  if (type === "float" && typeof value === "number") {
    const round = kind === "float32" ? Math.fround : (n: number) => n;
    const v = round(value);
    const b = round(Number(bound));
    if (v < b) return -1;
    return v > b ? 1 : 0;
  }
  if (type === "int" || type === "decimal") return compareDecimals(value, bound) ?? 0;
  return 0;
}

// Integer ranges a JavaScript number holds exactly; 64-bit kinds are the
// server's to range-check.
const INT_RANGES: Record<string, [number, number]> = {
  int8: [-128, 127], int16: [-32768, 32767], int32: [-2147483648, 2147483647],
  uint8: [0, 255], uint16: [0, 65535], uint32: [0, 4294967295],
};

function scalarOK(v: unknown, type: string, kind?: string): boolean {
  switch (type) {
    case "date":
      return isCivilDate(v);
    case "string":
    case "timestamp":
    case "uuid":
      return typeof v === "string";
    case "bool":
      return typeof v === "boolean";
    case "int": {
      if (typeof v !== "number" || !Number.isInteger(v)) return false;
      const range = kind ? INT_RANGES[kind] : undefined;
      return !range || (v >= range[0] && v <= range[1]);
    }
    case "float":
      return typeof v === "number" && Number.isFinite(v);
    case "decimal":
      return isDecimal(v);
    default:
      return true; // documents and other shapes are the server's to check
  }
}

/** Escape one RFC 6901 reference token: "~" then "/". */
function escapePointerToken(token: string): string {
  return token.replaceAll("~", "~0").replaceAll("/", "~1");
}

/** A one-token pointer back to its key, for messages. */
function unescapePointer(pointer: string): string {
  return pointer.slice(1).replaceAll("~1", "/").replaceAll("~0", "~");
}

/** Mirrors the Go parser's merge-patch operation count. */
function mergeOperations(obj: Record<string, unknown>, depth: number): number {
  let ops = 0;
  for (const value of Object.values(obj)) {
    const nested = typeof value === "object" && value !== null && !Array.isArray(value);
    if (depth === 1 || nested) ops++;
    if (nested) ops += mergeOperations(value as Record<string, unknown>, depth + 1);
  }
  return ops;
}
