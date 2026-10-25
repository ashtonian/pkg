import type { ErrorBody } from "./types.js";

/** A non-2xx response. `code` is the server's E-code ("" when the body was not JSON). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: ErrorBody;

  constructor(status: number, body: ErrorBody) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.body = body;
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

/** Schema validation error thrown by client-side validators. */
export class SchemaValidationError extends Error {
  readonly field: string;
  readonly code: string;
  readonly detail: string;

  constructor(field: string, code: string, detail: string) {
    super(`${code}: ${detail} (field: ${field})`);
    this.name = "SchemaValidationError";
    this.field = field;
    this.code = code;
    this.detail = detail;
  }
}
