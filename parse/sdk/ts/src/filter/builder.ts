import type { FilterExpression, FilterOperators, SortField } from "./types.js";

/** A filter expression, or a builder whose {@link FilterBuilder.build} is one. */
export type Filterable<T> = FilterExpression<T> | FilterBuilder<T>;

function expression<T>(f: Filterable<T>): FilterExpression<T> {
  return f instanceof FilterBuilder ? f.build() : f;
}

function isEmpty(expr: object): boolean {
  return Object.keys(expr).length === 0;
}

// The members of a group of `op` to splice into an enclosing group of the
// same op, or undefined for an expression that is more than that group.
function groupMembers<T>(expr: FilterExpression<T>, op: "$and" | "$or"): FilterExpression<T>[] | undefined {
  const keys = Object.keys(expr);
  return keys.length === 1 && keys[0] === op ? expr[op] : undefined;
}

function group<T>(op: "$and" | "$or", exprs: readonly Filterable<T>[]): FilterExpression<T> {
  const members: FilterExpression<T>[] = [];
  for (const f of exprs) {
    const expr = expression(f);
    if (isEmpty(expr)) continue;
    members.push(...(groupMembers(expr, op) ?? [expr]));
  }
  if (members.length === 0) return {};
  if (members.length === 1 && members[0]) return members[0];
  return { [op]: members } as FilterExpression<T>;
}

/**
 * The rows every expression matches: `{ $and: [a, b] }`. Empty expressions
 * match every row and are dropped, so `and()` is `{}`; nested `$and`s
 * flatten, so a filter built up in a loop stays within the server's nesting
 * depth.
 */
export function and<T>(...exprs: Filterable<T>[]): FilterExpression<T> {
  return group("$and", exprs);
}

/**
 * The rows any expression matches: `{ $or: [a, b] }`. An expression that is
 * empty matches every row, and so does the `or` holding it; nested `$or`s
 * flatten. `or()` of nothing would match no row, which the grammar cannot
 * express: it throws a `RangeError`.
 */
export function or<T>(...exprs: Filterable<T>[]): FilterExpression<T> {
  if (exprs.length === 0) {
    throw new RangeError("or() needs at least one filter: a disjunction of nothing matches no row");
  }
  const built = exprs.map(expression);
  if (built.some(isEmpty)) return {};
  return group("$or", built);
}

/**
 * The rows `expr` does not match: `{ $not: expr }`. The empty filter matches
 * every row, so its negation would match none, which the grammar cannot
 * express: it throws a `RangeError`.
 */
export function not<T>(expr: Filterable<T>): FilterExpression<T> {
  const built = expression(expr);
  if (isEmpty(built)) {
    throw new RangeError("not() of the empty filter matches no row");
  }
  return { $not: built } as FilterExpression<T>;
}

// An operator object holds operators only: `{ $gte: 1 }`. Anything else is
// a bare value, implicitly `$eq` (a document value included).
function isOperatorObject(v: unknown): v is Record<string, unknown> {
  if (v === null || typeof v !== "object" || Array.isArray(v)) return false;
  const keys = Object.keys(v);
  return keys.length > 0 && keys.every((k) => k.startsWith("$"));
}

/**
 * An immutable conjunction of field conditions: each {@link FilterBuilder.where}
 * returns a new builder. Combine builders and expressions with {@link and},
 * {@link or} and {@link not}.
 */
export class FilterBuilder<T> {
  private readonly conditions: FilterExpression<T>;

  /** @internal Use {@link filter}. */
  constructor(conditions: FilterExpression<T> = {}) {
    this.conditions = conditions;
  }

  /**
   * Add a condition on a field; a bare value is implicit `$eq`. A second
   * condition on the same field merges its operators into the first
   * (`{ $gte: 1, $lte: 5 }`); an operator the field already has, or a second
   * condition beside a bare value, throws a `RangeError`, so no call drops a
   * condition already added.
   */
  where<K extends keyof T & string>(
    field: K,
    value: T[K] | FilterOperators<T[K]>,
  ): FilterBuilder<T> {
    // defineProperty keeps a field named __proto__ an own member.
    const next = { ...this.conditions } as Record<string, unknown>;
    if (!Object.hasOwn(next, field)) {
      Object.defineProperty(next, field, { value, enumerable: true, writable: true, configurable: true });
      return new FilterBuilder<T>(next as FilterExpression<T>);
    }
    const current = next[field];
    if (!isOperatorObject(current) || !isOperatorObject(value)) {
      throw new RangeError(`field "${field}" already has a condition that a bare value cannot share; combine them with and()`);
    }
    for (const op of Object.keys(value)) {
      if (Object.hasOwn(current, op)) {
        throw new RangeError(`field "${field}" already has ${op}; combine the conditions with and() or or()`);
      }
    }
    Object.defineProperty(next, field, { value: { ...current, ...value }, enumerable: true, writable: true, configurable: true });
    return new FilterBuilder<T>(next as FilterExpression<T>);
  }

  /** The filter expression: each field's conditions, conjoined. */
  build(): FilterExpression<T> {
    return { ...this.conditions };
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
