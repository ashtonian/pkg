// The JSON number grammar: a decimal's wire form is a JSON number or, for an
// exact value, a string in that grammar (shopspring's decimal marshals one).
const DECIMAL = /^-?(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/;

/** The most digits a decimal holds in plain notation (Go core.MaxDecimalDigits). */
const MAX_DECIMAL_DIGITS = 1000;

const INT32_MIN = -2147483648;
const INT32_MAX = 2147483647;

/**
 * A decimal is a finite number, or a string in the JSON number grammar whose
 * plain notation, leading and trailing zeros dropped, has at most 1000
 * digits — the values Go core.DecimalText accepts.
 */
export function isDecimal(value: unknown): value is number | string {
  if (typeof value === "number") return Number.isFinite(value);
  if (typeof value !== "string") return false;
  const match = DECIMAL.exec(value);
  if (!match) return false;
  const intPart = match[1] ?? "";
  const frac = match[2] ?? "";
  const exp = match[3] === undefined ? 0 : Number(match[3]);
  if (exp < INT32_MIN || exp > INT32_MAX) return false;
  return plainWidth(intPart + frac, intPart.length + exp) <= MAX_DECIMAL_DIGITS;
}

// plainWidth is the digit count of digits × 10^(point − digits.length) in
// plain notation: the "0" of "0.…" and the zeros between the point and the
// first significant digit included, as Go's renderDecimal counts them.
function plainWidth(digits: string, point: number): number {
  let lead = 0;
  while (lead < digits.length && digits[lead] === "0") lead++;
  if (lead === digits.length) return 1;
  let trail = digits.length;
  while (digits[trail - 1] === "0") trail--;
  const significant = trail - lead;
  const at = point - lead;
  if (at <= 0) return 1 - at + significant;
  return Math.max(at, significant);
}
