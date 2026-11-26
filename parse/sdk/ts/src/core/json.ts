import { NonFiniteNumberError, type Payload, UnencodableValueError } from "./errors.js";

declare const encoded: unique symbol;

/** JSON text that {@link encodeJSON} wrote: the only form a request takes a JSON payload in. */
export type JSONText = string & { readonly [encoded]: true };

/**
 * The JSON text of a request payload, as JSON.stringify writes it, except
 * that a value JSON.stringify would write as null though it is not null
 * throws {@link UnencodableValueError} naming its member, as Go's
 * encoding/json refuses one: a number JSON cannot carry (NaN, Infinity,
 * -Infinity) as {@link NonFiniteNumberError}, an invalid Date or any other
 * toJSON that returns null, and undefined, a function or a symbol in an
 * array. Every JSON payload the client sends is encoded here, so nothing
 * the caller did not write as null becomes one: a removal in a merge patch,
 * a NULL comparison in a filter.
 */
export function encodeJSON(value: unknown, payload: Payload): JSONText {
  // A non-finite number is written as null, so text without "null" holds
  // none and keeps JSON.stringify's native path, which a replacer leaves.
  // Text with one is written again by the checking walk, whose text is the
  // one sent.
  const text = JSON.stringify(value);
  if (!text.includes("null")) return text as JSONText;
  return encodeChecked(value, payload);
}

// A member's place: the object holding it and its key there.
interface Place {
  readonly holder: object;
  readonly key: string;
}

function encodeChecked(value: unknown, payload: Payload): JSONText {
  // JSON.stringify calls the replacer with the object holding the member as
  // `this`. Each object it enters is recorded with its place before its
  // members are visited, so a member's pointer is built from those places,
  // and only for a member refused. The one holder never entered is the
  // wrapper JSON.stringify holds the root in.
  const places = new Map<object, Place>();
  const pointer = (place: Place): string => {
    const parent = places.get(place.holder);
    return parent === undefined ? "" : `${pointer(parent)}/${escapePointerToken(place.key)}`;
  };
  // The replacer sees a member after its toJSON ran; `this[key]` is the
  // value the caller passed.
  const text = JSON.stringify(value, function (this: object, key: string, member: unknown): unknown {
    const refuse = (description: string, original: unknown): never => {
      throw new UnencodableValueError(payload, pointer({ holder: this, key }), original, description);
    };
    // A Number object is written as the number it holds.
    const number = typeof member === "number" ? member : member instanceof Number ? member.valueOf() : undefined;
    if (number !== undefined && !Number.isFinite(number)) {
      throw new NonFiniteNumberError(payload, pointer({ holder: this, key }), number);
    }
    const original: unknown = (this as Record<string, unknown>)[key];
    if (member === null && original !== null) {
      refuse(original instanceof Date ? "an invalid Date" : "a value whose toJSON() is null", original);
    }
    // An object leaves such a member out; an array writes null in its place.
    if (Array.isArray(this) && (member === undefined || typeof member === "function" || typeof member === "symbol")) {
      refuse(`${typeof member} in an array`, original);
    }
    if (typeof member === "object" && member !== null) places.set(member, { holder: this, key });
    return member;
  });
  return text as JSONText;
}

/** One RFC 6901 reference token: "~" then "/" escaped. */
export function escapePointerToken(token: string): string {
  return token.replaceAll("~", "~0").replaceAll("/", "~1");
}
