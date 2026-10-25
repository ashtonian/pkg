import type { FilterExpression, FilterOperators, SortField } from "./types.js";

/** Fluent builder for constructing type-safe filter expressions. */
export class FilterBuilder<T> {
  private conditions: FilterExpression<T> = {};
  private logicalOrs: FilterExpression<T>[] = [];
  private logicalAnds: FilterExpression<T>[] = [];
  private negation: FilterExpression<T> | undefined;

  /** Add a field condition. Bare value is implicit $eq. */
  where<K extends keyof T & string>(
    field: K,
    value: T[K] | FilterOperators<T[K]>,
  ): this {
    this.conditions = { ...this.conditions, [field]: value };
    return this;
  }

  /** Combine with $or. */
  or(...branches: Array<FilterBuilder<T> | FilterExpression<T>>): this {
    for (const b of branches) {
      this.logicalOrs.push(b instanceof FilterBuilder ? b.build() : b);
    }
    return this;
  }

  /** Combine with $and. */
  and(...branches: Array<FilterBuilder<T> | FilterExpression<T>>): this {
    for (const b of branches) {
      this.logicalAnds.push(b instanceof FilterBuilder ? b.build() : b);
    }
    return this;
  }

  /** Negate with $not. */
  not(expr: FilterBuilder<T> | FilterExpression<T>): this {
    this.negation = expr instanceof FilterBuilder ? expr.build() : expr;
    return this;
  }

  /** Produce the final filter expression object. */
  build(): FilterExpression<T> {
    const result: FilterExpression<T> = { ...this.conditions };
    if (this.logicalOrs.length > 0) {
      (result as Record<string, unknown>).$or = [...this.logicalOrs];
    }
    if (this.logicalAnds.length > 0) {
      (result as Record<string, unknown>).$and = [...this.logicalAnds];
    }
    if (this.negation) {
      (result as Record<string, unknown>).$not = this.negation;
    }
    return result;
  }
}

/** Start a new type-safe filter builder. */
export function filter<T>(): FilterBuilder<T> {
  return new FilterBuilder<T>();
}

/** Fluent sort builder. */
export class SortBuilder<T> {
  private fields: SortField<T>[] = [];

  asc(field: keyof T & string): this {
    this.fields.push({ field, order: "asc" });
    return this;
  }

  desc(field: keyof T & string): this {
    this.fields.push({ field, order: "desc" });
    return this;
  }

  build(): SortField<T>[] {
    return [...this.fields];
  }

  toString(): string {
    return this.fields
      .map((s) => (s.order === "desc" ? `-${s.field}` : s.field))
      .join(",");
  }
}

/** Start a new type-safe sort builder. */
export function sort<T>(): SortBuilder<T> {
  return new SortBuilder<T>();
}
