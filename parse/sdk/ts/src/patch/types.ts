import type { FieldType } from "../core/types.js";
import type { Wire } from "../core/wire.js";

/** Patch operation type (mirrors Go patch.PatchOp). */
export type PatchOp = "add" | "remove" | "replace" | "move" | "copy" | "test" | "array_append";

/** Patch schema describing patchable paths. */
export interface PatchSchema {
  paths: Record<string, PatchPathSchema>;
  max_operations: number;
  /** The most keys one operation on a relation path names, repeats
   *  counted once (E2119); 0 or absent is unlimited. */
  max_relation_values?: number;
}

/** A single patchable path (mirrors Go parse.PatchPathResponse). */
export interface PatchPathSchema {
  allowed_ops: PatchOp[];
  data_type: FieldType;
  /**
   * "string" for a path whose value is a JSON string holding a bool,
   * integer or float ("42"), as the field reads in responses
   * (json:",string"); absent, values take the data type's own JSON form.
   */
  wire?: Wire;
  /** The path accepts null (remove, or clear). */
  nullable?: boolean;
  /** The path's allowlist. */
  enum_values?: string[];
  /**
   * The fewest and most characters (Unicode code points) of a string path,
   * or of each element of an array path.
   */
  min_length?: number;
  max_length?: number;
  /**
   * The inclusive bounds of a number path, or of each element of an array
   * path, as exact decimal text.
   */
  minimum?: string;
  maximum?: string;
  /** The fewest and most elements of an array path. */
  min_items?: number;
  max_items?: number;
  /** An array path's element type. */
  elem?: FieldType;
  /** The Go numeric kind of a number path, or of an array path's elements. */
  number_kind?: string;
  /** A relation's membership path: its value is a list of target keys. */
  relation?: boolean;
  /**
   * The target key's columns, in key order, of a membership path whose
   * target key has several: each key is an array of a value per column.
   * A one-column key's type is `elem`'s.
   */
  key?: RelationKeyColumn[];
}

/** A target key column of a membership path (mirrors Go parse.RelationKeyResponse). */
export interface RelationKeyColumn {
  data_type: FieldType;
  /** "string" for an integer column whose value is a JSON string holding it (json:",string"). */
  wire?: Wire;
  number_kind?: string;
}

/**
 * JSON Merge Patch (RFC 7396) input for a row of type T: any subset of its
 * fields, each a {@link MergeValue} of the field's type, so a JSON document
 * field takes the members to change and its others keep their stored
 * values. A field set to null removes it; the server takes that on a field
 * its schema marks `nullable`, which `validateBeforeSend` checks.
 */
export type PatchInput<T> = {
  [K in keyof T]?: MergeValue<T[K]> | null;
};

/**
 * A merge patch of a JSON object of type T: any subset of its members, each
 * a {@link MergeValue} of the member's type. null removes a member T may
 * lack: one it declares optional (`label?: string`) or names by an index
 * signature (a map's entry). A member T always carries takes no null, since
 * the server's type would write it back and so refuses its removal (E2077).
 * A merge into an optional member or a map entry the stored document lacks
 * must carry every member its type requires, which only the server can
 * judge.
 */
export type MergeDocument<T> = {
  [K in keyof T]?: MayLack<T, K> extends true
    ? MergeValue<T[K]> | null
    : MergeValue<Exclude<T[K], null>>;
};

// A member T may lack: a key of an index signature, or one T declares optional.
type MayLack<T, K extends keyof T> =
  string extends K ? true
    : number extends K ? true
      : Pick<T, K> extends Required<Pick<T, K>> ? false : true;

/**
 * A merge patch's value for a member of type T. A JSON object merges into
 * the stored one, as a {@link MergeDocument}. An array replaces the stored
 * array whole, so it keeps its full element type. Any other value replaces
 * the stored one as itself, as does an object JSON.stringify writes through
 * its `toJSON`, such as a Date.
 */
export type MergeValue<T> =
  T extends readonly unknown[] ? T
    : T extends { toJSON(): unknown } ? T
      : T extends object ? MergeDocument<T>
        : T;

/** Result of a patch, as parse's `parsehttp.PatchAnswer` writes it. */
export interface PatchResult<T> {
  updated: T[];
  failed?: BatchItemError[];
}

/** One entity a batch patch could not update. */
export interface BatchItemError {
  id: string;
  code: string;
  error: string;
}
