// Shared types used by both filter and patch sub-packages.

/** Field data type (mirrors Go core.FieldType). */
export type FieldType =
  | "string"
  | "int"
  | "float"
  | "decimal"
  | "bool"
  | "timestamp"
  | "date"
  | "time"
  | "interval"
  | "inet"
  | "cidr"
  | "macaddr"
  | "uuid"
  | "json"
  | "array"
  | "object"
  | "any"
  | "geo";

/**
 * Error body a parse-backed server returns (Go `parsehttp.Body`): an E-code, a
 * message, the JSON Pointer of the patch member (`path`) and the position of
 * the JSON Patch operation (`index`) a patch error is about, and optional
 * structured details.
 */
export interface ErrorBody {
  code: string;
  message: string;
  path?: string;
  index?: number;
  details?: Record<string, unknown>;
}

/** One key column's value: a string as itself, a finite number, a bigint
 *  or a boolean as its text. */
export type KeyValue = string | number | bigint | boolean;

/**
 * A row's key: one value for a key of one column, or a tuple of a value per
 * key column, in key order, for a key of several (the schema document's
 * `key`). An item URL carries a path segment per value.
 */
export type ResourceKey = KeyValue | readonly [KeyValue, ...KeyValue[]];

/** Per-request options. */
export interface RequestOptions {
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

/** Options for one list request. */
export interface ListRequestOptions extends RequestOptions {
  /** The list's transport: `"GET"` sends its parameters in the query
   *  string, `"POST"` the same request as a JSON body to the same path,
   *  which the server must route to its list handler (Go
   *  `parsehttp.ParseList` or `Resource.ParseListPost`). Unset, a list is a
   *  GET unless `ClientConfig.maxURLLength` is set and its URL would be
   *  longer. */
  method?: "GET" | "POST";
}
