/**
 * Unified schema validator combining filter, sort, and patch validation.
 * Wraps the server-side schema and provides both validation and introspection.
 *
 * @example
 * ```ts
 * const v = await products.validator();
 * v.filterableFields();   // ["id", "name", "category", "price"]
 * v.facetableFields();    // ["category", "price"]
 * v.fieldsByGroup();      // Map { "browse" => [category, price] }
 * ```
 * @module
 */

import { SchemaValidationError } from "./core/errors.js";
import type { FilterFieldSchema, FilterOperator } from "./filter/types.js";
import { type RelatedSchemas, relatedTargets, validateFilter } from "./filter/validate.js";
import { validateSort as validateSortImpl } from "./filter/validate.js";
import { validatePatch } from "./patch/validate.js";
import type { ResourceSchema } from "./schema.js";

export { SchemaValidationError };
export { relatedTargets, validateFilter } from "./filter/validate.js";
export type { RelatedSchemas } from "./filter/validate.js";
export { validatePatch } from "./patch/validate.js";
export { validateSort } from "./filter/validate.js";

/**
 * A pre-bound validator for a specific resource schema.
 * Provides both validation (throws on invalid input) and introspection
 * (lists available fields, operators, enums, facets, etc.).
 */
export class SchemaValidator {
  constructor(private readonly schema: ResourceSchema) {}

  private filterField(name: string): FilterFieldSchema | undefined {
    const fields = this.schema.filter?.fields;
    return fields && Object.hasOwn(fields, name) ? fields[name] : undefined;
  }

  /**
   * Validate a filter expression. Throws {@link SchemaValidationError} on invalid fields or operators.
   * `related` supplies relation targets' schemas, so their nested filters validate too.
   */
  validateFilter(filter: Record<string, unknown>, related?: RelatedSchemas): void {
    if (!this.schema.filter) {
      throw new SchemaValidationError("*", "E2078", "No filter schema registered for this resource");
    }
    validateFilter(filter, this.schema.filter, related);
  }

  /** Relation targets whose schemas the filter's nested relation filters need, beyond those `related` knows. */
  relatedTargets(filter: Record<string, unknown>, related?: RelatedSchemas): string[] {
    return this.schema.filter ? relatedTargets(filter, this.schema.filter, related) : [];
  }

  /** Validate a JSON Merge Patch. Throws {@link SchemaValidationError} on invalid fields or operations. */
  validatePatch(input: Record<string, unknown>): void {
    if (!this.schema.patch) {
      throw new SchemaValidationError("*", "E2078", "No patch schema registered for this resource");
    }
    validatePatch(input, this.schema.patch);
  }

  /** Validate a sort string (e.g. `"-price,name"`). Throws on unknown fields or exceeding max sort fields. */
  validateSort(sortStr: string): void {
    if (!this.schema.sort) {
      throw new SchemaValidationError("*", "E2078", "No sort schema registered for this resource");
    }
    validateSortImpl(sortStr, this.schema.sort);
  }

  /** Validate `include` fields. Throws unless each is an includable relation field (E2071 / E2083, as the server). */
  validateInclude(fields: readonly string[]): void {
    for (const field of fields) {
      const f = this.filterField(field);
      if (!f) {
        throw new SchemaValidationError(field, "E2071", `include field '${field}' does not exist`);
      }
      if (!f.includable) {
        throw new SchemaValidationError(field, "E2083", `field '${field}' is not an includable relation`);
      }
    }
  }

  /** Validate `facets` fields. Throws unless each is a facetable field (E2071, as the server). */
  validateFacets(fields: readonly string[]): void {
    for (const field of fields) {
      if (!this.filterField(field)?.facetable) {
        throw new SchemaValidationError(field, "E2071", `facet field '${field}' does not exist or is not facetable`);
      }
    }
  }

  /** Returns all field names that can be used in filter expressions. */
  filterableFields(): string[] {
    return this.schema.filter ? Object.keys(this.schema.filter.fields) : [];
  }

  /** Returns the allowed filter operators for a specific field. */
  operatorsFor(field: string): FilterOperator[] {
    return this.filterField(field)?.operators ?? [];
  }

  /** Returns the allowed enum values for a field, or empty array if not an enum. */
  enumValuesFor(field: string): string[] {
    return this.filterField(field)?.enum_values ?? [];
  }

  /** Returns all field names that can be modified via patch. */
  patchableFields(): string[] {
    if (!this.schema.patch) return [];
    return Object.keys(this.schema.patch.paths).map((p) => p.slice(1).replaceAll("~1", "/").replaceAll("~0", "~"));
  }

  /** Returns all field names that can be used in sort expressions. */
  sortableFields(): string[] {
    return this.schema.sort ? Object.keys(this.schema.sort.fields) : [];
  }

  /** Returns field names that support faceted aggregation (sidebar counts). */
  facetableFields(): string[] {
    if (!this.schema.filter) return [];
    return Object.entries(this.schema.filter.fields)
      .filter(([, f]) => f.facetable)
      .map(([k]) => k);
  }

  /** Returns relation fields a list can project with `include`. */
  includableFields(): string[] {
    if (!this.schema.filter) return [];
    return Object.entries(this.schema.filter.fields)
      .filter(([, f]) => f.includable)
      .map(([k]) => k);
  }

  /** Returns field names included in full-text search. */
  searchableFields(): string[] {
    return this.schema.search?.fields ?? [];
  }

  /** Returns true if full-text search is enabled for this resource. */
  isSearchEnabled(): boolean {
    return this.schema.search?.enabled ?? false;
  }

  /**
   * Returns UI metadata for a filter field: label, group, order, description.
   * Returns undefined if the field doesn't exist.
   */
  fieldMeta(field: string): { label?: string | undefined; group?: string | undefined; order?: number | undefined; description?: string | undefined } | undefined {
    const f = this.filterField(field);
    if (!f) return undefined;
    return { label: f.label, group: f.group, order: f.order, description: f.description };
  }

  /**
   * Returns all filter fields grouped by their `group` tag, sorted by `order` within each group.
   * Useful for rendering filter sidebar sections.
   *
   * @example
   * ```ts
   * const groups = v.fieldsByGroup();
   * // Map { "browse" => [category, price], "details" => [condition, rating] }
   * for (const [group, fields] of groups) {
   *   renderSection(group, fields);
   * }
   * ```
   */
  fieldsByGroup(): Map<string, FilterFieldSchema[]> {
    const groups = new Map<string, FilterFieldSchema[]>();
    if (!this.schema.filter) return groups;
    for (const f of Object.values(this.schema.filter.fields)) {
      const group = f.group ?? "";
      const arr = groups.get(group) ?? [];
      arr.push(f);
      groups.set(group, arr);
    }
    for (const arr of groups.values()) {
      arr.sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
    }
    return groups;
  }
}

