// Combined schema response (mirrors Go parse.SchemaResponse).
// This file imports from sub-packages to compose the full resource schema.

import type { FilterSchema, SearchSchema, SortSchema } from "./filter/types.js";
import type { PatchSchema } from "./patch/types.js";

/** The schema document format this client reads (Go `parse.SchemaVersion`). */
export const SCHEMA_VERSION = 1;

/** Combined schema for a single resource: the server's schema document. */
export interface ResourceSchema {
  /** The document's format version; the client refuses any but {@link SCHEMA_VERSION}. */
  version: number;
  resource: string;
  filter?: FilterSchema;
  patch?: PatchSchema;
  sort?: SortSchema;
  search?: SearchSchema;
}
