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

// own sets a member on a plain object as an own data property, so a member
// named __proto__ stays a member rather than the object's prototype.
function own(target: Record<string, unknown>, key: string, value: unknown): void {
  Object.defineProperty(target, key, { value, enumerable: true, writable: true, configurable: true });
}

// snapshot copies the plain data of an operand (objects, arrays, Dates) so
// that neither the caller's later changes nor a change to what build()
// returned reaches the builder. Any other object (a class instance with a
// toJSON, such as a decimal) is held as it is: it is the caller's value.
function snapshot(value: unknown): unknown {
  if (value instanceof Date) return new Date(value.getTime());
  if (Array.isArray(value)) return value.map(snapshot);
  if (typeof value !== "object" || value === null) return value;
  const proto: unknown = Object.getPrototypeOf(value);
  if (proto !== Object.prototype && proto !== null) return value;
  const copy: Record<string, unknown> = {};
  for (const [key, member] of Object.entries(value)) own(copy, key, snapshot(member));
  return copy;
}

/**
 * An immutable conjunction of field conditions: each {@link FilterBuilder.where}
 * returns a new builder. Operands are copied when added and the expression
 * when built, so changing a value after passing it, or the expression
 * build() returned, changes no builder. Combine builders and expressions with {@link and},
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
    const next = { ...this.conditions } as Record<string, unknown>;
    const added = snapshot(value);
    if (!Object.hasOwn(next, field)) {
      own(next, field, added);
      return new FilterBuilder<T>(next as FilterExpression<T>);
    }
    const current = next[field];
    if (!isOperatorObject(current) || !isOperatorObject(added)) {
      throw new RangeError(`field "${field}" already has a condition that a bare value cannot share; combine them with and()`);
    }
    for (const op of Object.keys(added)) {
      if (Object.hasOwn(current, op)) {
        throw new RangeError(`field "${field}" already has ${op}; combine the conditions with and() or or()`);
      }
    }
    own(next, field, { ...current, ...added });
    return new FilterBuilder<T>(next as FilterExpression<T>);
  }

  /** The filter expression, each field's conditions conjoined: a copy the caller may change. */
  build(): FilterExpression<T> {
    return snapshot(this.conditions) as FilterExpression<T>;
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
