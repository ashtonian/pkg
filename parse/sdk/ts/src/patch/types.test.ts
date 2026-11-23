// PatchInput's contract. The compilers (`pnpm run check`, `check:compat`)
// hold the type cases: a line under @ts-expect-error must not compile, and
// every other call must. The calls that compile also run, through a client
// whose fetch records each body, so a typed call is seen to send its input
// unchanged. The server's side, the sibling a partial merge leaves, is
// parse's TestTypedMergePatchPreservesSiblings.

import { describe, expect, expectTypeOf, it } from "vitest";

import { createClient } from "../client.js";
import type { MergeDocument, MergeValue, PatchInput } from "./types.js";

interface Stop {
  city: string;
  units: string;
}

// A JSON document as the server's Go type writes it: the members it always
// writes are required (note as null when its pointer is nil), a member it
// may leave out (omitempty) optional, a map an index signature.
interface Prefs {
  city: string;
  units: string;
  label?: string;
  note: string | null;
  display: { theme: string; density: string; accent?: string };
  stops: Stop[];
  counts: Record<string, number>;
}

interface Row {
  id: string;
  score: number | null;
  prefs: Prefs;
  tags: string[];
}

function recorder() {
  const bodies: unknown[] = [];
  const fetchFn: typeof globalThis.fetch = (_input, init) => {
    bodies.push(init?.body);
    return Promise.resolve(new Response(JSON.stringify({ updated: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
  };
  return { bodies, rows: createClient({ baseURL: "https://api.test", fetch: fetchFn }).resource<Row>("rows") };
}

describe("PatchInput", () => {
  it("takes any subset of a JSON document's members, at any depth, and sends it unchanged", async () => {
    const { bodies, rows } = recorder();
    const inputs: PatchInput<Row>[] = [];
    const send = async (input: PatchInput<Row>) => {
      inputs.push(input);
      await rows.patch("one", input);
    };

    // One member; its sibling is left to keep its stored value.
    await rows.patch("one", { prefs: { city: "Bath" } });
    expect(bodies).toEqual(['{"prefs":{"city":"Bath"}}']);
    await rows.patchMany(["one", "two"], { prefs: { city: "Bath" } });
    expect(bodies[1]).toBe('{"prefs":{"city":"Bath"}}');
    bodies.length = 0;

    await send({ prefs: { display: { theme: "dark" } } });
    // An optional member set, another removed; a map entry set, another removed.
    await send({ prefs: { label: "home", display: { accent: null }, counts: { visits: 3, stale: null } } });
    // A member read as null, given a value.
    await send({ prefs: { note: "call" } });
    // An array replaces the stored one whole, its elements complete.
    await send({ prefs: { stops: [{ city: "Bath", units: "metric" }] }, tags: ["a"] });
    // A complete document is still a merge document.
    await send({ prefs: { city: "Bath", units: "metric", note: "call", display: { theme: "dark", density: "compact" }, stops: [], counts: {} } });
    // A field's null is the server schema's to allow, as before.
    await send({ score: null, prefs: null });
    expect(bodies).toEqual(inputs.map((input) => JSON.stringify(input)));
  });

  it("refuses what the merge would get wrong, at compile time", () => {
    const { rows } = recorder();
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { city: "Bath" } });
    // @ts-expect-error a member keeps its leaf type
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { city: 1 } });
    // @ts-expect-error several levels down too
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { display: { theme: false } } });
    // @ts-expect-error an array is replaced whole, so each element needs every member
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { stops: [{ city: "Bath" }] } });
    // @ts-expect-error a scalar array keeps its element type
    expectTypeOf(rows.patch).toBeCallableWith("one", { tags: [1] });
    // @ts-expect-error a map entry keeps its value type
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { counts: { visits: "3" } } });
    // @ts-expect-error the document declares no such member
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { cty: "Bath" } });
    // @ts-expect-error a member the document always carries cannot be removed
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { units: null } });
    // @ts-expect-error nor one it carries as null, which null would remove rather than set
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { note: null } });
    // @ts-expect-error nor one several levels down
    expectTypeOf(rows.patch).toBeCallableWith("one", { prefs: { display: { density: null } } });
    // @ts-expect-error patchMany takes the same input
    expectTypeOf(rows.patchMany).toBeCallableWith(["one"], { prefs: { city: 1 } });
  });

  it("keeps leaves and arrays as their types, and merges objects", () => {
    expectTypeOf<MergeValue<string>>().toEqualTypeOf<string>();
    expectTypeOf<MergeValue<number | null>>().toEqualTypeOf<number | null>();
    expectTypeOf<MergeValue<Date>>().toEqualTypeOf<Date>();
    expectTypeOf<MergeValue<Stop[]>>().toEqualTypeOf<Stop[]>();
    expectTypeOf<MergeValue<readonly Stop[]>>().toEqualTypeOf<readonly Stop[]>();
    expectTypeOf<MergeValue<Stop>>().toEqualTypeOf<MergeDocument<Stop>>();
    expectTypeOf<MergeDocument<Stop>>().toEqualTypeOf<{ city?: string; units?: string }>();
    expectTypeOf<MergeDocument<{ note?: string }>>().toEqualTypeOf<{ note?: string | null }>();
    expectTypeOf<MergeDocument<Record<string, number>>>().toEqualTypeOf<Record<string, number | null | undefined>>();
    expectTypeOf<MergeDocument<{ at: string | null }>>().toEqualTypeOf<{ at?: string }>();
  });
});
