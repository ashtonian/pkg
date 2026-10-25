// Shared types used by both filter and patch sub-packages.

/** Field data type (mirrors Go core.FieldType). */
export type FieldType =
  | "string"
  | "int"
  | "float"
  | "bool"
  | "timestamp"
  | "uuid"
  | "json"
  | "array"
  | "object"
  | "any"
  | "geo";

/**
 * Error body a parse-backed server returns: an E-code, a message, and optional
 * structured details.
 */
export interface ErrorBody {
  code: string;
  message: string;
  details?: Record<string, unknown>;
  trace_id?: string;
  timestamp?: string;
  path?: string;
}

/** Per-request options. */
export interface RequestOptions {
  headers?: Record<string, string>;
  signal?: AbortSignal;
}
