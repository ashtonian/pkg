/**
 * API client for servers built with the parse package.
 * Provides typed list, filter, sort, facet, search, and patch operations
 * with optional client-side schema validation.
 * @module
 */

import { ApiError, ResponseError, SchemaResourceError, SchemaValidationError, SchemaVersionError } from "./core/errors.js";
import { hasProblemMembers, isProblemMediaType, isRecord, PROBLEM_MEDIA_TYPE, type Problem, readProblem } from "./core/problem.js";
import type { ErrorBody, KeyValue, ListRequestOptions, RequestOptions, ResourceKey } from "./core/types.js";
import type { FacetOptions, FacetResult, FilterSchema, ListOptions, ListResult, SortField } from "./filter/types.js";
import type { RelatedSchemas } from "./filter/validate.js";
import type { PatchInput, PatchResult } from "./patch/types.js";
import { type ResourceSchema, SCHEMA_VERSION } from "./schema.js";
import { SchemaValidator } from "./validate.js";

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// A document in another format could rename or redefine a member the
// validators read, so it would validate requests wrongly rather than fail.
function checkVersion(schema: ResourceSchema): ResourceSchema {
  if (schema.version !== SCHEMA_VERSION) {
    throw new SchemaVersionError(schema.resource, schema.version, SCHEMA_VERSION);
  }
  return schema;
}

function encodeSort<T>(sort: SortField<T>[] | string): string {
  if (typeof sort === "string") return sort;
  return sort
    .map((s) => (s.order === "desc" ? `-${s.field}` : s.field))
    .join(",");
}

function hasFilter(f: object | undefined): f is Record<string, unknown> {
  return f !== undefined && Object.keys(f).length > 0;
}

// Query parameters the server's list handler reads (filter.Param*). Zero
// members (an empty sort or name list, page size 0) are left out, so the
// server applies its defaults, as the Go client does.
function buildListQuery<T>(opts: ListOptions<T>): string {
  const params = new URLSearchParams();
  if (hasFilter(opts.filter)) {
    params.set("filter", JSON.stringify(opts.filter));
  }
  const sort = opts.sort === undefined ? "" : encodeSort(opts.sort);
  if (sort) {
    params.set("sort", sort);
  }
  if (opts.cursor) {
    params.set("cursor", opts.cursor);
  }
  if (opts.pageSize) {
    params.set("page_size", String(opts.pageSize));
  }
  if (opts.includeTotal) {
    params.set("include_total", "true");
  }
  if (opts.includeDeleted) {
    params.set("include_deleted", "true");
  }
  if (opts.search) {
    params.set("search", opts.search);
  }
  if (opts.include && opts.include.length > 0) {
    params.set("include", opts.include.join(","));
  }
  if (opts.facets && opts.facets.length > 0) {
    params.set("facets", opts.facets.join(","));
  }
  if (opts.fields && opts.fields.length > 0) {
    params.set("fields", opts.fields.join(","));
  }
  return query(params);
}

// The same request as one JSON document, a POST body the server reads with
// Resource.ParseListBody: the query parameters as members, the filter as a
// document and the name lists as arrays; zero members are left out.
function buildListBody<T>(opts: ListOptions<T>): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  if (hasFilter(opts.filter)) body["filter"] = opts.filter;
  const sort = opts.sort === undefined ? "" : encodeSort(opts.sort);
  if (sort) body["sort"] = sort;
  if (opts.cursor) body["cursor"] = opts.cursor;
  if (opts.pageSize) body["page_size"] = opts.pageSize;
  if (opts.includeTotal) body["include_total"] = true;
  if (opts.includeDeleted) body["include_deleted"] = true;
  if (opts.search) body["search"] = opts.search;
  if (opts.include && opts.include.length > 0) body["include"] = [...opts.include];
  if (opts.facets && opts.facets.length > 0) body["facets"] = [...opts.facets];
  if (opts.fields && opts.fields.length > 0) body["fields"] = [...opts.fields];
  return body;
}

// trimSlashes is a URL path segment, or several, without the slashes that
// would double or end it.
function trimSlashes(s: string): string {
  return s.replace(/^\/+|\/+$/g, "");
}

// joinURL joins a base URL and path segments with exactly one slash between
// each, leaving out empty segments.
function joinURL(base: string, ...segments: string[]): string {
  const parts = segments.map(trimSlashes).filter((p) => p !== "");
  const root = base.replace(/\/+$/, "");
  return parts.length === 0 ? root : `${root}/${parts.join("/")}`;
}

function query(params: URLSearchParams): string {
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

// keyValues are a key's values, one per key column.
function keyValues(key: ResourceKey): readonly KeyValue[] {
  return isCompositeKey(key) ? key : [key];
}

function isCompositeKey(key: ResourceKey): key is readonly [KeyValue, ...KeyValue[]] {
  return Array.isArray(key);
}

// keyText is a key value as the server reads its column's text (Go
// Resource.ParseKey). A type check guards untyped callers, whose object
// would otherwise travel as "[object Object]".
function keyText(value: KeyValue): string {
  switch (typeof value) {
    case "string":
      return value;
    case "number":
      if (!Number.isFinite(value)) {
        throw new RangeError(`a key value must be a finite number, got ${String(value)}`);
      }
      return String(value);
    case "bigint":
    case "boolean":
      return String(value);
    default:
      throw new TypeError(`a key value is a string, number, bigint or boolean, got ${typeof value}`);
  }
}

// keySegment is a key value as one path segment, escaped on its own so a
// "/" in it stays in its segment. URL parsing drops "." and ".." from a path,
// escaped or not, and an empty segment is no segment, so no URL addresses a
// value spelt so.
function keySegment(value: KeyValue): string {
  const text = keyText(value);
  if (text === "" || text === "." || text === "..") {
    throw new RangeError(`a key value cannot be the path segment ${JSON.stringify(text)}`);
  }
  return encodeURIComponent(text);
}

// The server's item route has a segment per key column, so a key of another
// arity names a path it does not route.
function checkKeyArity(resource: string, columns: readonly string[], values: number): void {
  if (columns.length > 0 && columns.length !== values) {
    throw new RangeError(`the key of ${resource} is (${columns.join(", ")}), a value per column; got ${String(values)}`);
  }
}

function isErrorBody(v: Record<string, unknown>): v is Record<string, unknown> & ErrorBody {
  return typeof v["code"] === "string" && typeof v["message"] === "string";
}

// A problem's code is its `code` extension, else its type; about:blank
// states nothing beyond the HTTP status, so it is no code.
function problemBody(problem: Problem, fallbackMessage: string): ErrorBody {
  const extension = problem.extensions["code"];
  let code = "";
  if (typeof extension === "string" && extension !== "") {
    code = extension;
  } else if (problem.type !== "about:blank") {
    code = problem.type;
  }
  return { code, message: problem.detail || problem.title || fallbackMessage };
}

// A body in parse's shape keeps its own code, message and details, and is
// also read as a problem when it is one. A body in neither shape (a proxy's
// HTML error page, an empty body, unrelated JSON) keeps only the status.
async function readApiError(res: Response): Promise<ApiError> {
  const statusMessage = res.statusText || `HTTP ${String(res.status)}`;
  let doc: unknown;
  try {
    doc = JSON.parse(await res.text());
  } catch {
    doc = undefined;
  }
  if (!isRecord(doc)) {
    return new ApiError(res.status, { code: "", message: statusMessage });
  }
  const isProblem = isProblemMediaType(res.headers.get("Content-Type")) || hasProblemMembers(doc);
  const problem = isProblem ? readProblem(doc, res.url) : undefined;
  if (isErrorBody(doc)) {
    return new ApiError(res.status, doc, problem);
  }
  if (problem) {
    return new ApiError(res.status, problemBody(problem, statusMessage), problem);
  }
  return new ApiError(res.status, { code: "", message: statusMessage });
}

// A 2xx is read as text once, so a body that is empty or not JSON is a
// ResponseError carrying the status, never a bare SyntaxError.
async function parseResponse<R>(res: Response, url: string): Promise<R> {
  if (!res.ok) {
    throw await readApiError(res);
  }
  const text = await res.text();
  if (text.trim() === "") {
    throw new ResponseError(res.status, url, "the response has no body");
  }
  try {
    return JSON.parse(text) as R;
  } catch {
    throw new ResponseError(res.status, url, `the body is not JSON (Content-Type ${res.headers.get("Content-Type") ?? "unset"})`);
  }
}

const MAX_TIMEOUT_MS = 2 ** 32 - 1;

// ---------------------------------------------------------------------------
// Client configuration
// ---------------------------------------------------------------------------

/** Configuration for {@link createClient}. */
export interface ClientConfig {
  /** Base URL of the API: origin and prefix, such as
   *  `"https://api.example.com/api"`. A resource's path is relative to it. */
  baseURL: string;
  /** Default headers sent with every request (e.g. Authorization). Names
   *  are case-insensitive; a request's own headers replace them. */
  headers?: Record<string, string>;
  /** Custom fetch implementation. Defaults to `globalThis.fetch`. */
  fetch?: typeof globalThis.fetch;
  /** Path below `baseURL` under which the server serves schema documents:
   *  `{schemaPrefix}/{resource}/schema` and `{schemaPrefix}/schemas`.
   *  Unset, schemas are beside the collections,
   *  `{baseURL}/{resource}/schema`, where parse's example server serves
   *  them. Leading and trailing slashes are ignored. */
  schemaPrefix?: string;
  /** Validate filters, sorts, and patches against the server schema before sending. */
  validateBeforeSend?: boolean;
  /** Per-request timeout in milliseconds: a positive integer up to
   *  2^32 - 1. Defaults to 30000. The deadline is enforced with
   *  `AbortSignal.timeout()` and combined with any caller-provided signal,
   *  so whichever fires first wins. */
  timeout?: number;
  /** Opts `list` and `listAll` into the POST body transport: the longest
   *  list URL sent as a GET, in characters, past which the same request is
   *  sent as a JSON body in a POST to the same path. Proxies and load
   *  balancers in front of a server cap URL length (8 KB is common) below
   *  what a filter may hold. Unset, every list is a GET unless a call passes
   *  `{ method: "POST" }`.
   *
   *  Set it only once the server routes POST lists to the collection path,
   *  through Go `parsehttp.ParseList` or `Resource.ParseListPost`: a server
   *  without that route answers 405, or hands the list to its create
   *  handler. `facets` and `patchMany` always send in the URL. Must be a
   *  positive integer. */
  maxURLLength?: number;
}

// ---------------------------------------------------------------------------
// Resource handle
// ---------------------------------------------------------------------------

/**
 * A typed handle to a single REST resource collection.
 * Provides list (with filter, sort, facets, search, includes, pagination), patch,
 * schema discovery, and client-side validation.
 *
 * `K` is the type of a row's key: one value by default, a tuple of a value
 * per key column, in key order, for a key of several (`[string, string]`
 * for the schema `key` `["warehouse_id", "sku"]`), so a key of another
 * arity does not compile.
 *
 * Its methods do not depend on their receiver (`this: void`): a method
 * destructured from the handle, or passed as a callback, makes the requests
 * a call through the handle makes, with validation on or off.
 */
export interface Resource<T, K extends ResourceKey = KeyValue> {
  /**
   * Fetch a filtered, sorted, paginated list with optional facets and count.
   * When `validateBeforeSend` is enabled, validates filter and sort against the schema.
   * A GET, unless `reqOpts.method` or {@link ClientConfig.maxURLLength}
   * selects the POST body transport.
   */
  list(this: void, opts?: ListOptions<T>, reqOpts?: ListRequestOptions): Promise<ListResult<T>>;

  /**
   * Auto-paginate through all pages. Yields one {@link ListResult} per page.
   * Follows cursors automatically until no more pages. Each page takes the
   * transport {@link Resource.list} would. Nothing is sent until the first
   * page is asked for.
   */
  listAll(this: void, opts?: Omit<ListOptions<T>, "cursor">, reqOpts?: ListRequestOptions): AsyncIterableIterator<ListResult<T>>;

  /**
   * Partially update the row `key` names with a JSON Merge Patch (RFC
   * 7396): set a field to replace it (objects merge), set it to null to
   * remove it. The request is `PATCH {collection}/{value}/…`, a path segment
   * per key value in key order, each escaped on its own
   * (`encodeURIComponent`), so `["w1", "a/b"]` is `/w1/a%2Fb`. A key of no
   * value, or a value no path segment can carry (`""`, `"."`, `".."`), throws
   * `RangeError` before sending; so does, under `validateBeforeSend`, a key
   * whose number of values is not the schema's number of `key` columns.
   */
  patch(this: void, key: K, input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>>;

  /**
   * Apply one JSON Merge Patch to several rows (`PATCH ?id[]=…`), each named
   * by the one value of a key of one column. `id[]` has no form for a key of
   * several columns: a tuple key throws `RangeError` before sending, as does,
   * under `validateBeforeSend`, any call on a resource whose schema `key` has
   * several columns; patch such rows one by one with {@link Resource.patch}.
   * The ids always travel in the URL: {@link ClientConfig.maxURLLength} does
   * not apply, and the server contract has no body form for them.
   */
  patchMany(this: void, ids: readonly Extract<K, KeyValue>[], input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>>;

  /**
   * Facet counts for facetable fields, optionally under a filter
   * (`OPTIONS ?facets=`). The filter always travels in the URL:
   * {@link ClientConfig.maxURLLength} does not apply. For counts under a
   * filter too large for a URL, ask {@link Resource.list} for `facets`
   * over POST; a page carries the counts of the whole matched population.
   */
  facets(this: void, fields: (keyof T & string)[], opts?: FacetOptions<T>, reqOpts?: RequestOptions): Promise<FacetResult>;

  /**
   * Fetch the server-side schema for this resource. Cached after first call.
   * Concurrent calls share one request, sent with the first call's headers
   * under the client's timeout; a call's `signal` ends only that call's wait,
   * and the request is cancelled once every waiting call has aborted.
   */
  schema(this: void, reqOpts?: RequestOptions): Promise<ResourceSchema>;

  /** Get a {@link SchemaValidator} for client-side validation and introspection. */
  validator(this: void, reqOpts?: RequestOptions): Promise<SchemaValidator>;
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

/** Options for {@link Client.resource}. */
export interface ResourceOptions {
  /** The resource's registry name: the server's name for its schema
   *  document (Go: its table), which relation targets name too. Its schema
   *  is fetched from `{baseURL}/{schemaPrefix}/{schema}/schema`. Defaults
   *  to the path's last segment, for a collection served under its
   *  registry name. */
  schema?: string;
}

/** Top-level API client. Use {@link createClient} to create one. Its
 *  methods, like a {@link Resource}'s, do not depend on their receiver. */
export interface Client {
  /** Create a typed resource handle for a collection path below `baseURL`;
   *  `K` is its rows' key type ({@link Resource}). An empty path throws a
   *  `RangeError`. */
  resource<T, K extends ResourceKey = KeyValue>(this: void, path: string, opts?: ResourceOptions): Resource<T, K>;

  /** Fetch schemas for all registered resources. Populates the schema
   *  cache, keyed by each document's registry name. */
  schemas(this: void, reqOpts?: RequestOptions): Promise<ResourceSchema[]>;
}

/**
 * Create an API client for servers built with the parse package.
 *
 * @example
 * ```ts
 * const client = createClient({
 *   baseURL: "https://api.example.com",
 *   headers: { Authorization: `Bearer ${token}` },
 *   validateBeforeSend: true,
 * });
 * const products = client.resource<Product>("products");
 * const page = await products.list({ filter: { status: "active" }, pageSize: 25 });
 * ```
 */
export function createClient(cfg: ClientConfig): Client {
  const fetchFn = cfg.fetch ?? globalThis.fetch;
  const schemaPrefix = trimSlashes(cfg.schemaPrefix ?? "");
  const validate = cfg.validateBeforeSend ?? false;
  const timeoutMs = cfg.timeout ?? 30_000;
  const maxURLLength = cfg.maxURLLength;
  // AbortSignal.timeout throws on each request for a delay outside this
  // range, and fires at once for 0: refuse both here, once.
  if (!(Number.isSafeInteger(timeoutMs) && timeoutMs > 0 && timeoutMs <= MAX_TIMEOUT_MS)) {
    throw new RangeError(`timeout must be a positive integer of milliseconds up to ${String(MAX_TIMEOUT_MS)}, got ${String(timeoutMs)}`);
  }
  // NaN or Infinity would silently keep every list a GET, and zero or less
  // silently send every list as a POST.
  if (maxURLLength !== undefined && !(Number.isSafeInteger(maxURLLength) && maxURLLength > 0)) {
    throw new RangeError(`maxURLLength must be a positive integer, got ${String(maxURLLength)}`);
  }

  // Headers' set is case-insensitive, so a caller's content-type replaces
  // the default rather than joining it. Content-Type goes only with a body:
  // on a bodiless GET it would make a cross-origin list a preflighted one.
  function requestHeaders(hasBody: boolean, extra?: Record<string, string>): Headers {
    const headers = new Headers({ Accept: `application/json, ${PROBLEM_MEDIA_TYPE}` });
    if (hasBody) headers.set("Content-Type", "application/json");
    for (const layer of [cfg.headers, extra]) {
      if (!layer) continue;
      for (const [name, value] of Object.entries(layer)) headers.set(name, value);
    }
    return headers;
  }

  // The caller's signal, if any, and the per-request timeout: whichever
  // fires first aborts the fetch.
  function combineSignals(callerSignal?: AbortSignal): AbortSignal {
    const timeoutSignal = AbortSignal.timeout(timeoutMs);
    if (!callerSignal) return timeoutSignal;
    return AbortSignal.any([callerSignal, timeoutSignal]);
  }

  async function request<R>(method: string, url: string, body?: unknown, reqOpts?: RequestOptions): Promise<R> {
    const hasBody = body != null;
    const init: RequestInit = {
      method,
      headers: requestHeaders(hasBody, reqOpts?.headers),
      signal: combineSignals(reqOpts?.signal),
    };
    if (hasBody) {
      init.body = JSON.stringify(body);
    }
    const res = await fetchFn(url, init);
    return parseResponse<R>(res, url);
  }

  function schemaURL(name: string): string {
    return joinURL(cfg.baseURL, schemaPrefix, encodeURIComponent(name), "schema");
  }

  // Keyed by registry name: a handle's, a relation target's and a listed
  // document's `resource` are the same name.
  const schemaCache = new Map<string, ResourceSchema>();

  // One schema request shared by every concurrent caller for a resource.
  interface SharedSchemaFetch {
    readonly schema: Promise<ResourceSchema>;
    readonly cancel: AbortController;
    waiters: number;
  }
  const schemaInflight = new Map<string, SharedSchemaFetch>();

  // Concurrent callers share one fetch; only a successful schema is cached,
  // so a failed fetch is retried by the next call. The shared request runs
  // under the client's timeout, never one caller's signal: a caller's abort
  // ends only its own wait, and the request is cancelled once no caller is
  // left waiting.
  async function fetchSchema(name: string, reqOpts?: RequestOptions): Promise<ResourceSchema> {
    const cached = schemaCache.get(name);
    if (cached) return cached;
    const signal = reqOpts?.signal;
    signal?.throwIfAborted();
    const shared = schemaInflight.get(name) ?? startSchemaFetch(name, reqOpts?.headers);
    return waitForSchema(name, shared, signal);
  }

  // The first caller's headers go with the shared request; the schema cache
  // is the client's, not any one caller's.
  function startSchemaFetch(name: string, headers: Record<string, string> | undefined): SharedSchemaFetch {
    const cancel = new AbortController();
    const opts: RequestOptions = { signal: cancel.signal };
    if (headers) opts.headers = headers;
    const fetched = request<ResourceSchema>("GET", schemaURL(name), undefined, opts);
    const shared: SharedSchemaFetch = {
      schema: fetched.then((doc) => {
        const schema = checkVersion(doc);
        if (schema.resource !== name) throw new SchemaResourceError(name, schema.resource);
        schemaCache.set(name, schema);
        return schema;
      }),
      cancel,
      waiters: 0,
    };
    const forget = () => {
      if (schemaInflight.get(name) === shared) schemaInflight.delete(name);
    };
    shared.schema.then(forget, forget);
    schemaInflight.set(name, shared);
    return shared;
  }

  async function waitForSchema(name: string, shared: SharedSchemaFetch, signal: AbortSignal | undefined): Promise<ResourceSchema> {
    shared.waiters++;
    // A caller without a signal never leaves, so the request is never cancelled under it.
    if (!signal) return shared.schema;
    // Whichever happens first decides, as with fetch: a schema that settled
    // before the abort is this caller's even if the abort follows at once.
    const abortedFirst = await new Promise<boolean>((resolve) => {
      const aborted = () => {
        resolve(true);
      };
      const settled = () => {
        signal.removeEventListener("abort", aborted);
        resolve(false);
      };
      signal.addEventListener("abort", aborted, { once: true });
      shared.schema.then(settled, settled);
    });
    if (abortedFirst) {
      shared.waiters--;
      if (shared.waiters === 0) {
        // A later call starts a fresh request rather than joining a cancelled one.
        if (schemaInflight.get(name) === shared) schemaInflight.delete(name);
        shared.cancel.abort(signal.reason);
      }
      signal.throwIfAborted();
    }
    return shared.schema;
  }

  // relatedSchemas fetches the schemas of the relation targets a filter's
  // nested relation filters name, two levels deep as the server allows.
  async function relatedSchemas(v: SchemaValidator, f: Record<string, unknown>, reqOpts?: RequestOptions): Promise<RelatedSchemas> {
    const known = new Map<string, FilterSchema>();
    const lookup: RelatedSchemas = (name) => known.get(name);
    for (let level = 0; level < 2; level++) {
      const missing = v.relatedTargets(f, lookup).filter((name) => !known.has(name));
      if (missing.length === 0) break;
      const schemas = await Promise.all(missing.map((name) => fetchSchema(name, reqOpts)));
      missing.forEach((name, i) => {
        const fs = schemas[i]?.filter;
        if (fs) known.set(name, fs);
      });
    }
    return lookup;
  }

  // validateRequest checks a list's or a facet request's members against the
  // schema, related schemas included, as the server would.
  async function validateRequest<T>(v: SchemaValidator, opts: ListOptions<T>, reqOpts?: RequestOptions): Promise<void> {
    if (hasFilter(opts.filter)) {
      v.validateFilter(opts.filter, await relatedSchemas(v, opts.filter, reqOpts));
    }
    const sort = opts.sort === undefined ? "" : encodeSort(opts.sort);
    if (sort) {
      v.validateSort(sort);
    }
    if (opts.include) {
      v.validateInclude(opts.include);
    }
    if (opts.facets) {
      v.validateFacets(opts.facets);
    }
    if (opts.search?.trim() && !v.isSearchEnabled()) {
      throw new SchemaValidationError("search", "E2067", "this resource has no search fields");
    }
  }

  return {
    resource<T, K extends ResourceKey = KeyValue>(path: string, resourceOpts: ResourceOptions = {}): Resource<T, K> {
      const collection = trimSlashes(path);
      if (collection === "") {
        throw new RangeError(`resource path must name a collection, got ${JSON.stringify(path)}`);
      }
      // A registry name is one segment (Go: a table), so a nested path's
      // last segment is its default.
      const name = resourceOpts.schema ?? collection.slice(collection.lastIndexOf("/") + 1);
      if (name === "" || name.includes("/")) {
        throw new RangeError(`resource schema name must be one non-empty segment, got ${JSON.stringify(name)}`);
      }
      const collectionURL = joinURL(cfg.baseURL, collection);
      let validatorInstance: SchemaValidator | undefined;

      // The operations close over the handle rather than reading `this`, so
      // a destructured method, or one passed as a callback, makes the calls
      // the handle does.
      async function schema(reqOpts?: RequestOptions): Promise<ResourceSchema> {
        return fetchSchema(name, reqOpts);
      }

      async function validator(reqOpts?: RequestOptions): Promise<SchemaValidator> {
        if (validatorInstance) return validatorInstance;
        const doc = await fetchSchema(name, reqOpts);
        validatorInstance = new SchemaValidator(doc);
        return validatorInstance;
      }

      async function list(opts: ListOptions<T> = {}, reqOpts?: ListRequestOptions): Promise<ListResult<T>> {
        if (validate) {
          await validateRequest(await validator(reqOpts), opts, reqOpts);
        }
        const target = `${collectionURL}${buildListQuery(opts)}`;
        const tooLong = maxURLLength !== undefined && target.length > maxURLLength;
        const method = reqOpts?.method ?? (tooLong ? "POST" : "GET");
        if (method === "POST") {
          return request<ListResult<T>>("POST", collectionURL, buildListBody(opts), reqOpts);
        }
        return request<ListResult<T>>("GET", target, undefined, reqOpts);
      }

      async function* listAll(opts: Omit<ListOptions<T>, "cursor"> = {}, reqOpts?: ListRequestOptions): AsyncIterableIterator<ListResult<T>> {
        let cursor: string | undefined;
        for (;;) {
          const page = await list(cursor === undefined ? opts : { ...opts, cursor }, reqOpts);
          yield page;
          const next = page.page_info.end_cursor;
          if (!page.page_info.has_next_page || !next) return;
          cursor = next;
        }
      }

      async function patch(key: K, input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>> {
        const values = keyValues(key);
        if (values.length === 0) {
          throw new RangeError("patch needs a key: PATCH without one targets the collection");
        }
        const segments = values.map(keySegment);
        if (validate) {
          const v = await validator(reqOpts);
          checkKeyArity(name, v.keyColumns(), values.length);
          v.validatePatch(input);
        }
        return request<PatchResult<T>>("PATCH", `${collectionURL}/${segments.join("/")}`, input, reqOpts);
      }

      async function patchMany(ids: readonly Extract<K, KeyValue>[], input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>> {
        if (ids.length === 0) {
          throw new RangeError("patchMany needs at least one id: PATCH without one targets no row");
        }
        // The type admits no tuple here; an untyped caller's still has no id[] form.
        if (ids.some((id) => Array.isArray(id))) {
          throw new RangeError("patchMany names each row by one id[] value, which cannot hold a key of several columns; patch each such row with patch(key)");
        }
        if (validate) {
          const v = await validator(reqOpts);
          const columns = v.keyColumns();
          if (columns.length > 1) {
            throw new RangeError(`patchMany cannot address ${name}: its key is (${columns.join(", ")}) and id[] holds one value per row; patch each row with patch(key)`);
          }
          v.validatePatch(input);
        }
        const params = new URLSearchParams();
        for (const id of ids) params.append("id[]", keyText(id));
        return request<PatchResult<T>>("PATCH", `${collectionURL}${query(params)}`, input, reqOpts);
      }

      async function facets(fields: (keyof T & string)[], opts: FacetOptions<T> = {}, reqOpts?: RequestOptions): Promise<FacetResult> {
        if (validate) {
          const listOpts: ListOptions<T> = { facets: fields };
          if (opts.filter) listOpts.filter = opts.filter;
          if (opts.search !== undefined) listOpts.search = opts.search;
          await validateRequest(await validator(reqOpts), listOpts, reqOpts);
        }
        const params = new URLSearchParams({ facets: fields.join(",") });
        if (hasFilter(opts.filter)) {
          params.set("filter", JSON.stringify(opts.filter));
        }
        if (opts.search) {
          params.set("search", opts.search);
        }
        if (opts.includeDeleted) {
          params.set("include_deleted", "true");
        }
        const res = await request<{ facets?: FacetResult }>("OPTIONS", `${collectionURL}${query(params)}`, undefined, reqOpts);
        return res.facets ?? {};
      }

      return { list, listAll, patch, patchMany, facets, schema, validator };
    },

    async schemas(reqOpts?: RequestOptions): Promise<ResourceSchema[]> {
      const resp = await request<{ schemas: ResourceSchema[] }>("GET", joinURL(cfg.baseURL, schemaPrefix, "schemas"), undefined, reqOpts);
      for (const s of resp.schemas) {
        schemaCache.set(s.resource, checkVersion(s));
      }
      return resp.schemas;
    },
  };
}
