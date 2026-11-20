// RFC 9457 problem details, read from an error response.

/** The RFC 9457 JSON problem details media type. */
export const PROBLEM_MEDIA_TYPE = "application/problem+json";

/**
 * An RFC 9457 problem details object. A standard member of the wrong JSON type
 * is dropped, as the RFC requires; every other member is kept, as sent, in
 * `extensions`. All of it is server-supplied, untrusted text.
 */
export interface Problem {
  /** The problem type URI, resolved against the response URL; `"about:blank"` when absent. */
  readonly type: string;
  readonly title?: string;
  /** The status the document states. Advisory: `ApiError.status` is the response's. */
  readonly status?: number;
  readonly detail?: string;
  /** URI of this occurrence, resolved against the response URL. */
  readonly instance?: string;
  readonly extensions: Readonly<Record<string, unknown>>;
}

/** One entry of a problem's `errors` extension (RFC 9457 §3), the members it names. */
export interface FieldError {
  /** JSON Pointer to the request member at fault, as sent (`"#/age"` or `"/age"`). */
  readonly pointer?: string;
  readonly detail?: string;
  /** The entry's own error code, such as a parse E-code. */
  readonly code?: string;
}

const STANDARD_MEMBERS = new Set(["type", "title", "status", "detail", "instance"]);

type Mutable<T> = { -readonly [K in keyof T]: T[K] };

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Reports whether a Content-Type header names the problem details media type. */
export function isProblemMediaType(contentType: string | null): boolean {
  const mediaType = contentType?.split(";", 1)[0]?.trim().toLowerCase();
  return mediaType === PROBLEM_MEDIA_TYPE;
}

/**
 * Reports whether a JSON object sent without the problem media type is still
 * shaped as one: it has a standard member of the right type.
 */
export function hasProblemMembers(doc: Record<string, unknown>): boolean {
  return typeof doc["type"] === "string" || typeof doc["title"] === "string" || typeof doc["status"] === "number";
}

/**
 * Reads a problem document. Relative `type` and `instance` URIs resolve
 * against `responseURL`, which an empty string leaves unresolved.
 */
export function readProblem(doc: Record<string, unknown>, responseURL: string): Problem {
  // fromEntries defines members, so one named "__proto__" stays an ordinary member.
  const extensions = Object.fromEntries(Object.entries(doc).filter(([name]) => !STANDARD_MEMBERS.has(name)));
  const problem: Mutable<Problem> = {
    type: resolveReference(stringMember(doc, "type") ?? "about:blank", responseURL),
    extensions,
  };
  const title = stringMember(doc, "title");
  if (title !== undefined) problem.title = title;
  const status = doc["status"];
  if (typeof status === "number" && Number.isInteger(status) && status >= 100 && status <= 599) {
    problem.status = status;
  }
  const detail = stringMember(doc, "detail");
  if (detail !== undefined) problem.detail = detail;
  const instance = stringMember(doc, "instance");
  if (instance !== undefined && instance !== "") problem.instance = resolveReference(instance, responseURL);
  return problem;
}

/**
 * The object entries of a problem's `errors` extension, with the members
 * {@link FieldError} names; other members stay in `problem.extensions`.
 */
export function problemFieldErrors(problem: Problem): FieldError[] {
  const entries: unknown = problem.extensions["errors"];
  if (!Array.isArray(entries)) return [];
  return (entries as unknown[]).filter(isRecord).map((entry) => {
    const fieldError: Mutable<FieldError> = {};
    const pointer = stringMember(entry, "pointer");
    if (pointer !== undefined) fieldError.pointer = pointer;
    const detail = stringMember(entry, "detail");
    if (detail !== undefined) fieldError.detail = detail;
    const code = stringMember(entry, "code");
    if (code !== undefined) fieldError.code = code;
    return fieldError;
  });
}

function stringMember(doc: Record<string, unknown>, name: string): string | undefined {
  const value = doc[name];
  return typeof value === "string" ? value : undefined;
}

function resolveReference(reference: string, base: string): string {
  if (base === "") return reference;
  try {
    return new URL(reference, base).href;
  } catch {
    return reference;
  }
}
