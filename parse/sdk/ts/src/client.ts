/**
 * API client for servers built with the parse package.
 * Provides typed list, filter, sort, facet, search, and patch operations
 * with optional client-side schema validation.
 * @module
 */

import { ApiError, SchemaVersionError } from "./core/errors.js";
import { hasProblemMembers, isProblemMediaType, isRecord, PROBLEM_MEDIA_TYPE, type Problem, readProblem } from "./core/problem.js";
import type { ErrorBody, ListRequestOptions, RequestOptions } from "./core/types.js";
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

// Query parameters the server's list handler reads (filter.Param*).
function buildListQuery<T>(opts: ListOptions<T>): string {
  const params = new URLSearchParams();
  if (opts.filter && Object.keys(opts.filter).length > 0) {
    params.set("filter", JSON.stringify(opts.filter));
  }
  if (opts.sort) {
    params.set("sort", encodeSort(opts.sort));
  }
  if (opts.cursor) {
    params.set("cursor", opts.cursor);
  }
  if (opts.pageSize != null) {
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
  return query(params);
}

// The same request as one JSON document, a POST body the server reads with
// Resource.ParseListBody: the query parameters as members, the filter as a
// document and the name lists as arrays.
function buildListBody<T>(opts: ListOptions<T>): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  if (opts.filter && Object.keys(opts.filter).length > 0) body["filter"] = opts.filter;
  if (opts.sort) body["sort"] = encodeSort(opts.sort);
  if (opts.cursor) body["cursor"] = opts.cursor;
  if (opts.pageSize != null) body["page_size"] = opts.pageSize;
  if (opts.includeTotal) body["include_total"] = true;
  if (opts.includeDeleted) body["include_deleted"] = true;
  if (opts.search) body["search"] = opts.search;
  if (opts.include && opts.include.length > 0) body["include"] = [...opts.include];
  if (opts.facets && opts.facets.length > 0) body["facets"] = [...opts.facets];
  return body;
}

function query(params: URLSearchParams): string {
  const qs = params.toString();
  return qs ? `?${qs}` : "";
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

async function parseResponse<R>(res: Response): Promise<R> {
  if (!res.ok) {
    throw await readApiError(res);
  }
  return (await res.json()) as R;
}

// ---------------------------------------------------------------------------
// Client configuration
// ---------------------------------------------------------------------------

/** Configuration for {@link createClient}. */
export interface ClientConfig {
  /** Base URL of the API (no trailing slash). */
  baseURL: string;
  /** Default headers sent with every request (e.g. Authorization). */
  headers?: Record<string, string>;
  /** Custom fetch implementation. Defaults to `globalThis.fetch`. */
  fetch?: typeof globalThis.fetch;
  /** Path prefix for schema discovery endpoints. Defaults to `"api"`. */
  schemaPath?: string;
  /** Validate filters, sorts, and patches against the server schema before sending. */
  validateBeforeSend?: boolean;
  /** Per-request timeout in milliseconds. Defaults to 30000. The deadline
   *  is enforced via `AbortSignal.timeout()` and combined with any
   *  caller-provided signal so whichever fires first wins. */
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
 */
export interface Resource<T> {
  /**
   * Fetch a filtered, sorted, paginated list with optional facets and count.
   * When `validateBeforeSend` is enabled, validates filter and sort against the schema.
   * A GET, unless `reqOpts.method` or {@link ClientConfig.maxURLLength}
   * selects the POST body transport.
   */
  list(opts?: ListOptions<T>, reqOpts?: ListRequestOptions): Promise<ListResult<T>>;

  /**
   * Auto-paginate through all pages. Yields one {@link ListResult} per page.
   * Follows cursors automatically until no more pages. Each page takes the
   * transport {@link Resource.list} would.
   */
  listAll(opts?: Omit<ListOptions<T>, "cursor">, reqOpts?: ListRequestOptions): AsyncIterableIterator<ListResult<T>>;

  /**
   * Partially update one resource with a JSON Merge Patch (RFC 7396): set a
   * field to replace it (objects merge), set it to null to remove it.
   */
  patch(id: string, input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>>;

  /**
   * Apply one JSON Merge Patch to several resources (`PATCH ?id[]=…`). The
   * ids always travel in the URL: {@link ClientConfig.maxURLLength} does
   * not apply, and the server contract has no body form for them.
   */
  patchMany(ids: string[], input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>>;

  /**
   * Facet counts for facetable fields, optionally under a filter
   * (`OPTIONS ?facets=`). The filter always travels in the URL:
   * {@link ClientConfig.maxURLLength} does not apply. For counts under a
   * filter too large for a URL, ask {@link Resource.list} for `facets`
   * over POST; a page carries the counts of the whole matched population.
   */
  facets(fields: (keyof T & string)[], opts?: FacetOptions<T>, reqOpts?: RequestOptions): Promise<FacetResult>;

  /**
   * Fetch the server-side schema for this resource. Cached after first call.
   * Concurrent calls share one request, sent with the first call's headers
   * under the client's timeout; a call's `signal` ends only that call's wait,
   * and the request is cancelled once every waiting call has aborted.
   */
  schema(reqOpts?: RequestOptions): Promise<ResourceSchema>;

  /** Get a {@link SchemaValidator} for client-side validation and introspection. */
  validator(reqOpts?: RequestOptions): Promise<SchemaValidator>;
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

/** Top-level API client. Use {@link createClient} to create one. */
export interface Client {
  /** Create a typed resource handle for a given API path. */
  resource<T>(path: string): Resource<T>;

  /** Fetch schemas for all registered resources. Populates the schema cache. */
  schemas(reqOpts?: RequestOptions): Promise<ResourceSchema[]>;
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
  const schemaPath = cfg.schemaPath ?? "api";
  const validate = cfg.validateBeforeSend ?? false;
  const timeoutMs = cfg.timeout ?? 30_000;
  const maxURLLength = cfg.maxURLLength;
  // NaN or Infinity would silently keep every list a GET, and zero or less
  // silently send every list as a POST.
  if (maxURLLength !== undefined && !(Number.isSafeInteger(maxURLLength) && maxURLLength > 0)) {
    throw new RangeError(`maxURLLength must be a positive integer, got ${String(maxURLLength)}`);
  }

  function mergeHeaders(extra?: Record<string, string>): Record<string, string> {
    return { "Content-Type": "application/json", Accept: `application/json, ${PROBLEM_MEDIA_TYPE}`, ...cfg.headers, ...extra };
  }

  /** Combine the caller's signal (if any) with the per-request timeout
   *  signal so cancellation from either side aborts the fetch. */
  function combineSignals(callerSignal?: AbortSignal): AbortSignal {
    const timeoutSignal = AbortSignal.timeout(timeoutMs);
    if (!callerSignal) return timeoutSignal;
    // `AbortSignal.any` is well-supported on all of our targets; if a
    // consumer needs older runtimes they can disable the timeout by
    // passing their own signal and `cfg.timeout = 0`-equivalent (a no-op
    // timeout would be ergonomically odd, so we don't expose that toggle
    // — they should use a runtime that supports the API).
    return AbortSignal.any([callerSignal, timeoutSignal]);
  }

  async function request<R>(method: string, path: string, body?: unknown, reqOpts?: RequestOptions): Promise<R> {
    const url = `${cfg.baseURL}/${path}`;
    const init: RequestInit = {
      method,
      headers: mergeHeaders(reqOpts?.headers),
      signal: combineSignals(reqOpts?.signal),
    };
    if (body != null) {
      init.body = JSON.stringify(body);
    }
    const res = await fetchFn(url, init);
    return parseResponse<R>(res);
  }

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
  async function fetchSchema(resourcePath: string, reqOpts?: RequestOptions): Promise<ResourceSchema> {
    const cached = schemaCache.get(resourcePath);
    if (cached) return cached;
    const signal = reqOpts?.signal;
    signal?.throwIfAborted();
    const shared = schemaInflight.get(resourcePath) ?? startSchemaFetch(resourcePath, reqOpts?.headers);
    return waitForSchema(resourcePath, shared, signal);
  }

  // The first caller's headers go with the shared request; the schema cache
  // is the client's, not any one caller's.
  function startSchemaFetch(resourcePath: string, headers: Record<string, string> | undefined): SharedSchemaFetch {
    const cancel = new AbortController();
    const opts: RequestOptions = { signal: cancel.signal };
    if (headers) opts.headers = headers;
    const fetched = request<ResourceSchema>("GET", `${schemaPath}/${resourcePath}/schema`, undefined, opts);
    const shared: SharedSchemaFetch = {
      schema: fetched.then((doc) => {
        const schema = checkVersion(doc);
        schemaCache.set(resourcePath, schema);
        return schema;
      }),
      cancel,
      waiters: 0,
    };
    const forget = () => {
      if (schemaInflight.get(resourcePath) === shared) schemaInflight.delete(resourcePath);
    };
    shared.schema.then(forget, forget);
    schemaInflight.set(resourcePath, shared);
    return shared;
  }

  async function waitForSchema(resourcePath: string, shared: SharedSchemaFetch, signal: AbortSignal | undefined): Promise<ResourceSchema> {
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
        if (schemaInflight.get(resourcePath) === shared) schemaInflight.delete(resourcePath);
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

  return {
    resource<T>(path: string): Resource<T> {
      let validatorInstance: SchemaValidator | undefined;

      return {
        async schema(reqOpts?: RequestOptions): Promise<ResourceSchema> {
          return fetchSchema(path, reqOpts);
        },

        async validator(reqOpts?: RequestOptions): Promise<SchemaValidator> {
          if (validatorInstance) return validatorInstance;
          const schema = await fetchSchema(path, reqOpts);
          validatorInstance = new SchemaValidator(schema);
          return validatorInstance;
        },

        async list(opts: ListOptions<T> = {}, reqOpts?: ListRequestOptions): Promise<ListResult<T>> {
          if (validate) {
            const v = await this.validator(reqOpts);
            if (opts.filter && Object.keys(opts.filter).length > 0) {
              v.validateFilter(opts.filter, await relatedSchemas(v, opts.filter, reqOpts));
            }
            if (opts.sort) {
              v.validateSort(typeof opts.sort === "string" ? opts.sort : encodeSort(opts.sort));
            }
            if (opts.include) {
              v.validateInclude(opts.include);
            }
            if (opts.facets) {
              v.validateFacets(opts.facets);
            }
          }
          const target = `${path}${buildListQuery(opts)}`;
          const tooLong = maxURLLength !== undefined && `${cfg.baseURL}/${target}`.length > maxURLLength;
          const method = reqOpts?.method ?? (tooLong ? "POST" : "GET");
          if (method === "POST") {
            return request<ListResult<T>>("POST", path, buildListBody(opts), reqOpts);
          }
          return request<ListResult<T>>("GET", target, undefined, reqOpts);
        },

        async *listAll(opts: Omit<ListOptions<T>, "cursor"> = {}, reqOpts?: ListRequestOptions): AsyncIterableIterator<ListResult<T>> {
          let cursor: string | undefined;
          for (;;) {
            const page = await this.list(cursor === undefined ? opts : { ...opts, cursor }, reqOpts);
            yield page;
            const next = page.page_info.end_cursor;
            if (!page.page_info.has_next_page || !next) return;
            cursor = next;
          }
        },

        async patch(id: string, input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>> {
          if (validate) {
            const v = await this.validator(reqOpts);
            v.validatePatch(input);
          }
          return request<PatchResult<T>>("PATCH", `${path}/${encodeURIComponent(id)}`, input, reqOpts);
        },

        async patchMany(ids: string[], input: PatchInput<T>, reqOpts?: RequestOptions): Promise<PatchResult<T>> {
          if (validate) {
            const v = await this.validator(reqOpts);
            v.validatePatch(input);
          }
          const params = new URLSearchParams();
          for (const id of ids) params.append("id[]", id);
          return request<PatchResult<T>>("PATCH", `${path}${query(params)}`, input, reqOpts);
        },

        async facets(fields: (keyof T & string)[], opts: FacetOptions<T> = {}, reqOpts?: RequestOptions): Promise<FacetResult> {
          if (validate && opts.filter && Object.keys(opts.filter).length > 0) {
            const v = await this.validator(reqOpts);
            v.validateFilter(opts.filter);
          }
          const params = new URLSearchParams({ facets: fields.join(",") });
          if (opts.filter && Object.keys(opts.filter).length > 0) {
            params.set("filter", JSON.stringify(opts.filter));
          }
          if (opts.search) {
            params.set("search", opts.search);
          }
          if (opts.includeDeleted) {
            params.set("include_deleted", "true");
          }
          const res = await request<{ facets?: FacetResult }>("OPTIONS", `${path}${query(params)}`, undefined, reqOpts);
          return res.facets ?? {};
        },
      };
    },

    async schemas(reqOpts?: RequestOptions): Promise<ResourceSchema[]> {
      const resp = await request<{ schemas: ResourceSchema[] }>("GET", `${schemaPath}/schemas`, undefined, reqOpts);
      for (const s of resp.schemas) {
        schemaCache.set(s.resource, checkVersion(s));
      }
      return resp.schemas;
    },
  };
}
