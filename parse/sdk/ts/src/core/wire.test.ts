import { describe, expect, it } from "vitest";

import { decimalInteger } from "./decimal.js";
import { fromWire } from "./wire.js";

describe("fromWire", () => {
  it("reads a quoted integer as its exact text", () => {
    expect(fromWire("int", "9223372036854775807")).toBe("9223372036854775807");
    expect(fromWire("int", "-9223372036854775808")).toBe("-9223372036854775808");
    expect(fromWire("int", "1e3")).toBe("1e3");
  });

  it("reads a quoted float and bool", () => {
    expect(fromWire("float", "0.5")).toBe(0.5);
    expect(fromWire("bool", "true")).toBe(true);
    expect(fromWire("bool", "false")).toBe(false);
  });

  it("refuses a bare value and text no JSON literal", () => {
    for (const [type, value] of [
      ["int", 7], ["int", " 7"], ["int", "+7"], ["int", "07"], ["int", "1_0"], ["int", "0x10"],
      ["int", "seven"], ["int", ""], ["bool", true], ["bool", "True"], ["bool", "1"], ["float", 0.5], ["string", "x"],
    ] as const) {
      expect(fromWire(type, value), `${type} ${JSON.stringify(value)}`).toBeUndefined();
    }
  });
});

describe("decimalInteger", () => {
  it("holds an integer exactly past 2^53", () => {
    expect(decimalInteger("9007199254740993")).toBe(9007199254740993n);
    expect(decimalInteger("-9223372036854775808")).toBe(-(2n ** 63n));
    expect(decimalInteger("18446744073709551615")).toBe(2n ** 64n - 1n);
  });

  it("reads integral spellings as Go does", () => {
    expect(decimalInteger("1.0")).toBe(1n);
    expect(decimalInteger("1e3")).toBe(1000n);
    expect(decimalInteger("1.5e1")).toBe(15n);
    expect(decimalInteger("-0")).toBe(0n);
  });

  it("refuses fractions and non-decimals", () => {
    expect(decimalInteger("1.5")).toBeUndefined();
    expect(decimalInteger("seven")).toBeUndefined();
    expect(decimalInteger("1e2000")).toBeUndefined();
  });
});
