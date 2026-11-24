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

/**
 * What a request method sends besides its method and path, and what it reads
 * back. `B` is the body's type and `R` the response's, both the caller's: no
 * schema checks either.
 */
export interface SendOptions<B = unknown, R = unknown> {
  /** Query parameters, appended to the URL as `URLSearchParams` writes them. */
  query?: URLSearchParams | Record<string, string>;
  /** The JSON body, sent as `application/json` through the encoder of every
   *  payload the client sends, so a non-finite number is a
   *  `NonFiniteNumberError` before anything is sent. Unset, the request has
   *  no body and no `Content-Type`. */
  body?: B;
  /** What a 2xx answer carries. `"json"`, the default: a JSON body, read as
   *  `R`; a 2xx without one (a 204, an empty 200) is a `ResponseError`, as
   *  for the resource methods. `"none"`: nothing the call reads; any 2xx
   *  resolves `undefined`, its body discarded. `"none"` is open only to an
   *  `R` that `undefined` is (`void`, `undefined`), since that is what the
   *  call resolves. */
  response?: undefined extends R ? "json" | "none" : "json";
}

/** {@link SendOptions} for a resource handle's request: the row it names and a path below it. */
export interface ResourceSendOptions<K extends ResourceKey = KeyValue, B = unknown, R = unknown> extends SendOptions<B, R> {
  /** The row: a path segment per key value below the collection, in key
   *  order, each escaped on its own, as `patch` sends it. Unset, the request
   *  is the collection's. */
  key?: K;
  /** A path below the collection, or below the row `key` names: one or more
   *  segments, sent as given, so escape a dynamic one with
   *  `encodeURIComponent`. Slashes at either end are ignored. */
  path?: string;
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
