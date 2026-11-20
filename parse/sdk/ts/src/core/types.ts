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

/** Options for one list request. */
export interface ListRequestOptions extends RequestOptions {
  /** The list's transport: `"GET"` sends its parameters in the query
   *  string, `"POST"` the same request as a JSON body to the same path.
   *  Unset, a list is a GET unless its URL would be longer than
   *  {@link ClientConfig.maxURLLength}. */
  method?: "GET" | "POST";
}
