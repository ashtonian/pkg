import { expect, test } from "vitest";
import { isCivilDate } from "./date.js";

test("civil dates are calendar values, without an instant or timezone", () => {
  for (const value of ["0001-01-01", "2028-02-29", "2000-02-29", "2026-10-26", "9999-12-31"]) expect(isCivilDate(value)).toBe(true);
  for (const value of [null, 20261026, "", "0000-01-01", "2026-02-29", "1900-02-29", "2026-02-30", "2026-1-01", "2026-10-26T00:00:00Z", "now-7d"]) expect(isCivilDate(value)).toBe(false);
});
