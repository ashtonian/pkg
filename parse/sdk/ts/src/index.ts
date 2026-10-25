/**
 * @ashtonian/parse — TypeScript SDK for the parse package.
 *
 * Provides type-safe filter building, sort building, schema validation,
 * faceted search support, and a fetch-based API client for servers built
 * with the Go parse package.
 *
 * @example
 * ```ts
 * import { createClient, filter, sort } from "@ashtonian/parse";
 *
 * const client = createClient({ baseURL: "https://api.example.com" });
 * const products = client.resource<Product>("products");
 *
 * const page = await products.list({
 *   filter: filter<Product>().where("status", "active").build(),
 *   sort: sort<Product>().desc("price").toString(),
 *   pageSize: 25,
 *   includeTotal: true,
 * });
 * page.page_info.end_cursor; // next page cursor
 * const counts = await products.facets(["category"]);
 * ```
 *
 * @packageDocumentation
 */

// Core (shared types and errors)
export { ApiError, SchemaValidationError, SchemaVersionError } from "./core/index.js";
export type { ErrorBody, FieldType, RequestOptions } from "./core/index.js";

// Filter
export { filter, FilterBuilder, sort, SortBuilder } from "./filter/index.js";
export { validateFilter, validateSort } from "./filter/index.js";
export type {
  FacetBucket,
  FacetOptions,
  FacetResult,
  FilterExpression,
  FilterFieldSchema,
  FilterOperator,
  FilterOperators,
  FilterSchema,
  GeoDistance,
  GeoPoint,
  GeoShape,
  ListOptions,
  ListResult,
  PageInfo,
  SearchSchema,
  SortDirection,
  SortField,
  SortFieldSchema,
  SortSchema,
} from "./filter/index.js";

// Patch
export { validatePatch } from "./patch/index.js";
export type {
  BatchItemError,
  PatchInput,
  PatchOp,
  PatchPathSchema,
  PatchResult,
  PatchSchema,
} from "./patch/index.js";

// Combined (client + schema + validator)
export { createClient } from "./client.js";
export type { Client, ClientConfig, Resource } from "./client.js";
export { SCHEMA_VERSION } from "./schema.js";
export type { ResourceSchema } from "./schema.js";
export { SchemaValidator } from "./validate.js";
