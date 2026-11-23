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
