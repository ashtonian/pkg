import { NonFiniteNumberError, type Payload } from "./errors.js";

declare const encoded: unique symbol;

/** JSON text that {@link encodeJSON} wrote: the only form a request takes a JSON payload in. */
export type JSONText = string & { readonly [encoded]: true };

/**
 * The JSON text of a request payload, as JSON.stringify writes it, except
 * that a number JSON cannot carry (NaN, Infinity, -Infinity), which
 * JSON.stringify would write as null, throws {@link NonFiniteNumberError}
 * naming its member, as Go's encoding/json refuses one. Every JSON payload
 * the client sends is encoded here, so none can turn a number into null: a
 * removal in a merge patch, a NULL comparison in a filter.
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
  const text = JSON.stringify(value, function (this: object, key: string, member: unknown): unknown {
    // A Number object is written as the number it holds.
    const number = typeof member === "number" ? member : member instanceof Number ? member.valueOf() : undefined;
    if (number !== undefined && !Number.isFinite(number)) {
      throw new NonFiniteNumberError(payload, pointer({ holder: this, key }), number);
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
