import { SchemaValidationError } from "../core/errors.js";
import type { PatchOp, PatchPathSchema, PatchSchema } from "./types.js";

/**
 * Validate a JSON Merge Patch against a PatchSchema as the Go parser does:
 * each key is a patchable top-level path (an RFC 6901 JSON Pointer), null
 * only where the path is nullable (and `remove` allowed), every other value
 * of the path's type — numbers integral and within the path's Go kind,
 * strings in its enum, arrays of its element type, a relation's membership
 * a list — and the operation count within the maximum, counting each
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

  for (const field of fields.toSorted()) {
    const path = `/${escapePointerToken(field)}`;
    const pathSchema = Object.hasOwn(schema.paths, path) ? schema.paths[path] : undefined;
    if (!pathSchema) {
      throw new SchemaValidationError(
        field, "E2076",
        `Field "${field}" is not patchable. Patchable fields: ${Object.keys(schema.paths).map(unescapePointer).join(", ")}`,
      );
    }
    const value = input[field];
    if (value === null && !pathSchema.nullable && !pathSchema.relation) {
      throw new SchemaValidationError(field, "E2077", `Field "${field}" cannot be null`);
    }
    const op: PatchOp = value === null ? "remove" : "replace";
    if (!pathSchema.allowed_ops.includes(op)) {
      throw new SchemaValidationError(
        field, "E2064",
        `Operation "${op}" is not allowed on "${field}". Allowed: ${pathSchema.allowed_ops.join(", ")}`,
      );
    }
    if (value !== null) validatePatchValue(field, value, pathSchema);
  }
}

function validatePatchValue(field: string, value: unknown, path: PatchPathSchema): void {
  if (path.relation) {
    if (!Array.isArray(value)) {
      throw new SchemaValidationError(field, "E2118", `Relation "${field}" takes a list of keys`);
    }
    return;
  }
  if (path.data_type === "array") {
    if (!Array.isArray(value)) {
      throw new SchemaValidationError(field, "E2077", `Field "${field}" expects an array`);
    }
    for (const el of value) {
      if (path.elem && !scalarOK(el, path.elem, path.number_kind)) {
        throw new SchemaValidationError(field, "E2077", `Field "${field}" expects ${path.elem} elements`);
      }
    }
    return;
  }
  if (!scalarOK(value, path.data_type, path.number_kind)) {
    throw new SchemaValidationError(field, "E2077", `Field "${field}" expects ${path.data_type}, got ${typeof value}`);
  }
  if (path.enum_values && path.enum_values.length > 0 && typeof value === "string" && !path.enum_values.includes(value)) {
    throw new SchemaValidationError(field, "E2077", `Value "${value}" is not allowed for "${field}". Allowed: ${path.enum_values.join(", ")}`);
  }
}

// Integer ranges a JavaScript number holds exactly; 64-bit kinds are the
// server's to range-check.
const INT_RANGES: Record<string, [number, number]> = {
  int8: [-128, 127], int16: [-32768, 32767], int32: [-2147483648, 2147483647],
  uint8: [0, 255], uint16: [0, 65535], uint32: [0, 4294967295],
};

function scalarOK(v: unknown, type: string, kind?: string): boolean {
  switch (type) {
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

