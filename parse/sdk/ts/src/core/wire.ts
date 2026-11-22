import type { FieldType } from "./types.js";

/**
 * A field's wire form, from its schema's `wire`: "string" for a bool,
 * integer or float field the server declares json:",string", whose values
 * travel as JSON strings holding their JSON text ("9007199254740993"), as
 * the field reads in responses.
 */
export type Wire = "string";

// The JSON number grammar (RFC 8259 §6), whole: no sign but "-", no spaces.
const JSON_NUMBER = /^-?(0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/;

/**
 * The value a string-wire operand holds, as Go core.FromWire reads it: the
 * JSON number text for an int (kept as text, so a 64-bit integer stays
 * exact), the number for a float, true or false for a bool; undefined when
 * the operand is no such string — a bare number or boolean included.
 */
export function fromWire(type: FieldType, value: unknown): string | number | boolean | undefined {
  if (typeof value !== "string") return undefined;
  switch (type) {
    case "int":
      return JSON_NUMBER.test(value) ? value : undefined;
    case "float":
      return JSON_NUMBER.test(value) ? Number(value) : undefined;
    case "bool":
      if (value === "true") return true;
      if (value === "false") return false;
      return undefined;
    default:
      return undefined;
  }
}
