import { expect, test } from "vitest";
import { compareDecimals, isDecimal } from "./decimal.js";

test("decimals are finite numbers or numeric strings in the JSON number grammar", () => {
  const valid = [
    0, -1, 10.25, 1e21, 5e-324,
    "0", "-0", "10", "10.50", "-1.5", "0.0012", "1e3", "1.5E+2", "125e-2", "0e9",
    "0.10000000000000001", "123456789012345678901234567890",
    "9".repeat(1000), "1e999", "1e-999", `1${"0".repeat(2000)}e-2000`,
  ];
  for (const value of valid) expect(isDecimal(value), String(value).slice(0, 40)).toBe(true);
});

test("non-decimals and decimals past 1000 plain digits are rejected", () => {
  const invalid = [
    null, undefined, true, {}, [], Number.NaN, Number.POSITIVE_INFINITY,
    "", "ten", "+1", ".5", "5.", "007", "--1", "0x10", "1/3", " 1", "1,5", "1e", "NaN", "Infinity",
    "9".repeat(1001), "1e1000", "1e-1000", "1e99999999999", "0e2147483648",
  ];
  for (const value of invalid) {
    expect(isDecimal(value), typeof value === "string" ? value.slice(0, 40) : typeof value).toBe(false);
  }
});

test("decimals compare exactly, whatever their spelling", () => {
  const cases: [unknown, unknown, -1 | 0 | 1][] = [
    ["1", "1.0", 0], ["10.50", "1.05e1", 0], ["-0", "0e9", 0], [0.1, "0.1", 0],
    ["999.990000000000000001", "999.99", 1], ["0.001", "0.01", -1], ["-1.5", "-1.25", -1],
    ["-2", "1", -1], ["123456789012345678901234567890", "123456789012345678901234567889", 1],
    [1e21, "1e21", 0], [`1${"0".repeat(2000)}e-2000`, "1", 0], ["1e999", "1e-999", 1],
  ];
  for (const [a, b, want] of cases) expect(compareDecimals(a, b), `${String(a).slice(0, 20)} vs ${String(b)}`).toBe(want);
});

test("comparing a non-decimal is undefined", () => {
  for (const value of ["ten", Number.NaN, null, "1e1000"]) expect(compareDecimals(value, "1")).toBeUndefined();
});
