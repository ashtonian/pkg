import type { FieldType } from "../core/types.js";

/** Patch operation type (mirrors Go patch.PatchOp). */
export type PatchOp = "add" | "remove" | "replace" | "move" | "copy" | "test" | "array_append";

/** Patch schema describing patchable paths. */
export interface PatchSchema {
  paths: Record<string, PatchPathSchema>;
  max_operations: number;
}

/** A single patchable path (mirrors Go parse.PatchPathResponse). */
export interface PatchPathSchema {
  allowed_ops: PatchOp[];
  data_type: FieldType;
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
}

/**
 * JSON Merge Patch (RFC 7396) input: a field set to a value replaces it
 * (objects merge recursively), a field set to null removes it.
 */
export type PatchInput<T> = {
  [K in keyof T]?: T[K] | null;
};

/** Result of a patch (mirrors Go repotypes.PatchResult). */
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
