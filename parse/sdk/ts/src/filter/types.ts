import type { FieldType } from "../core/types.js";

/** Filter operator (mirrors Go filter.FilterOperator). */
export type FilterOperator =
  | "eq" | "ne" | "gt" | "gte" | "lt" | "lte"
  | "in" | "nin" | "like" | "ilike"
  | "null" | "not_null" | "exists" | "contains" | "has_key"
  | "near" | "within" | "intersects"
  | "some" | "none" | "is" | "is_not";

/** A WGS 84 coordinate. */
export interface GeoPoint {
  lat: number;
  lon: number;
}

/** `$near` operand: within `distance` ("500m", "5km", "3mi") of a point. */
export interface GeoDistance extends GeoPoint {
  distance: string;
}

/** `$within` / `$intersects` operand: a box or a polygon (3–100 points). */
export type GeoShape =
  | { top_left: GeoPoint; bottom_right: GeoPoint }
  | { points: GeoPoint[] };

/** Filter operators on a field of value type V. */
export interface FilterOperators<V> {
  $eq?: V;
  $ne?: V;
  $gt?: V;
  $gte?: V;
  $lt?: V;
  $lte?: V;
  $in?: V[];
  $nin?: V[];
  /** `%` any run, `_` one character, `\` escapes the next character. */
  $like?: string;
  $ilike?: string;
  $null?: boolean;
  $not_null?: boolean;
  $exists?: boolean;
  /** String: literal substring. JSON: document containment. Array: holds every element. */
  $contains?: unknown;
  /** JSON document: has the top-level key, a string element of a top-level array, or is the string itself (jsonb `?`). */
  $has_key?: string;
  $near?: GeoDistance;
  $within?: GeoShape;
  $intersects?: GeoShape;
  /** Relation (has-many, many-to-many) or JSON array: some related row / element satisfies the filter. */
  $some?: Record<string, unknown>;
  /** Relation or JSON array: no related row / element satisfies the filter. */
  $none?: Record<string, unknown>;
  /** Belongs-to: the related row is visible and satisfies the filter. */
  $is?: Record<string, unknown>;
  /** Belongs-to: no visible related row satisfies the filter. */
  $is_not?: Record<string, unknown>;
}

/** A filter expression for resource type T. */
export type FilterExpression<T> = {
  [K in keyof T & string]?: T[K] | FilterOperators<T[K]>;
} & {
  $and?: FilterExpression<T>[];
  $or?: FilterExpression<T>[];
  $not?: FilterExpression<T>;
};

/** Sort direction. */
export type SortDirection = "asc" | "desc";

/** A single sort field + direction. */
export interface SortField<T> {
  field: keyof T & string;
  order?: SortDirection;
}

/** Filter schema describing filterable fields. */
export interface FilterSchema {
  fields: Record<string, FilterFieldSchema>;
}

/** A single filterable field with UI metadata (mirrors Go parse.FilterFieldResponse). */
export interface FilterFieldSchema {
  name: string;
  label?: string;
  description?: string;
  data_type: FieldType;
  operators: FilterOperator[];
  indexed?: boolean;
  immutable?: boolean;
  enum_values?: string[];
  facetable?: boolean;
  searchable?: boolean;
  /** A relation field whose related IDs a list can project (`include`). */
  includable?: boolean;
  group?: string;
  order?: number;
  geo_config?: { storage_type: string; srid: number };
  /** A relation field: its kind and, for a declared relation, the target resource whose filter `$some` / `$none` / `$is` / `$is_not` take. */
  relation?: { kind: "many_to_many" | "has_many" | "belongs_to"; target?: string };
  /** A JSON array's element paths, filtered together by `$some` / `$none`. */
  elements?: Record<string, FilterFieldSchema>;
}

/** Sort schema describing sortable fields. */
export interface SortSchema {
  fields: Record<string, SortFieldSchema>;
  default_field?: string;
  default_dir?: SortDirection;
  max_fields: number;
}

/** A single sortable field. */
export interface SortFieldSchema {
  indexed?: boolean;
}

/** Search schema describing full-text search configuration. */
export interface SearchSchema {
  enabled: boolean;
  fields: string[];
  param: string;
}

/** A single aggregation bucket (mirrors Go filter.FacetBucket). */
export interface FacetBucket {
  key: string;
  count: number;
}

/** Options accepted by a list call; each maps to one query parameter. */
export interface ListOptions<T> {
  /** `filter` — JSON filter expression. */
  filter?: FilterExpression<T>;
  /** `sort` — `"-price,name"`, or sort fields. */
  sort?: SortField<T>[] | string;
  /** `cursor` — a `page_info` cursor from a previous page. */
  cursor?: string;
  /** `page_size`. */
  pageSize?: number;
  /** `include_total` — populate `page_info.total_count`. */
  includeTotal?: boolean;
  /** `include_deleted` — include soft-deleted rows. */
  includeDeleted?: boolean;
  /** `search` — free-text search. */
  search?: string;
  /** `include` — relation fields whose related IDs the page carries in `_relations`. */
  include?: readonly string[];
  /** `facets` — facetable fields whose counts over the matched rows the page carries in `facets`. */
  facets?: readonly string[];
}

/** Pagination state (mirrors Go pagination.PageInfo). */
export interface PageInfo {
  has_next_page: boolean;
  has_prev_page: boolean;
  start_cursor?: string;
  end_cursor?: string;
  total_count?: number;
}

/** A list page (mirrors Go filter.Page). */
export interface ListResult<T> {
  items: T[];
  page_info: PageInfo;
  /** `include` projections: relation field → owner id → related ids. */
  _relations?: Record<string, Record<string, string[]>>;
  /** `include` owners whose related ids were cut at the per-owner cap: relation field → owner ids. */
  _truncated?: Record<string, string[]>;
  /** `facets` counts: field → buckets, terms by count then key, ranges as declared. */
  facets?: FacetResult;
}

/** Options for a facet request: the population whose counts to return. */
export interface FacetOptions<T> {
  filter?: FilterExpression<T>;
  /** Free-text search: counts describe the list the same search returns. */
  search?: string;
  includeDeleted?: boolean;
}

/** Facet counts per requested field. */
export type FacetResult = Record<string, FacetBucket[]>;
