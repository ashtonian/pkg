import { type FieldError, type Problem, problemFieldErrors } from "./problem.js";
import type { ErrorBody } from "./types.js";

/**
 * A non-2xx response. `code` and `message` come from a parse error body, or
 * from an RFC 9457 problem document; `code` is `""` when the body was neither.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** The response body in parse's error shape; a problem's `code` and `message` as mapped. */
  readonly body: ErrorBody;
  /** The problem document, when the response was one. */
  readonly problem: Problem | undefined;
  /** The entries of the problem's `errors` extension; empty otherwise. */
  readonly errors: readonly FieldError[];

  constructor(status: number, body: ErrorBody, problem?: Problem) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.body = body;
    this.problem = problem;
    this.errors = problem ? problemFieldErrors(problem) : [];
  }
}

/** A schema document in a format version this client does not read. */
export class SchemaVersionError extends Error {
  readonly resource: string;
  readonly version: unknown;
  readonly supported: number;

  constructor(resource: string, version: unknown, supported: number) {
    super(`schema document for ${resource} is version ${String(version)}; this client reads version ${String(supported)}`);
    this.name = "SchemaVersionError";
    this.resource = resource;
    this.version = version;
    this.supported = supported;
  }
}

/** Closed vocabulary of declared schema constraints. */
export type ConstraintName = "enum" | "minlen" | "maxlen" | "min" | "max" | "minitems" | "maxitems";

export interface ConstraintFailure {
  readonly constraint: ConstraintName;
  readonly limit: string;
}

/** Schema validation error thrown by client-side validators. */
export class SchemaValidationError extends Error {
  readonly field: string;
  readonly code: string;
  readonly detail: string;
  readonly constraint: ConstraintName | undefined;
  readonly limit: string | undefined;

  constructor(field: string, code: string, detail: string, constraint?: ConstraintFailure) {
    super(`${code}: ${detail} (field: ${field})`);
    this.name = "SchemaValidationError";
    this.field = field;
    this.code = code;
    this.detail = detail;
    this.constraint = constraint?.constraint;
    this.limit = constraint?.limit;
  }
}

/** Every schema failure, retaining the first failure's field/code contract. */
export class SchemaValidationErrors extends SchemaValidationError {
  readonly errors: readonly SchemaValidationError[];

  constructor(errors: readonly SchemaValidationError[]) {
    const first = errors[0];
    if (!first) throw new RangeError("SchemaValidationErrors requires at least one failure");
    const constraint = first.constraint !== undefined && first.limit !== undefined
      ? { constraint: first.constraint, limit: first.limit }
      : undefined;
    super(first.field, first.code, first.detail, constraint);
    this.name = "SchemaValidationErrors";
    this.errors = errors.slice();
    this.message = errors.map(error => error.message).join("\n");
  }
}
