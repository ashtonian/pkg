//go:build integration

// Package corpus is the database-backed suite parse's SQL providers run:
// every case builds resources on a provider over a fresh database and
// asserts the rows that come back, the rows stored and the statements
// sent. parsesql's tests run it on SQLite and parsepgx's on PostgreSQL
// (RunSQL), so each provider is held to the same cases. The fixtures the
// root package's own tests also use live in internal/parsetest.
package corpus

import (
	"slices"
	"testing"
)

// sqlCase is one case of the corpus: a subtest of RunSQL.
type sqlCase struct {
	name string
	// only is the one kind of engine the case runs on, for a feature of
	// that engine alone; empty runs it on every engine.
	only Kind
	run  func(t *testing.T, engine Engine)
}

// sqlCases are the corpus's cases, grouped by the file that holds them
// and sorted by name within it.
func sqlCases() []sqlCase {
	cases := []sqlCase{
		{name: "ExecutorContract", run: executorContract},

		{name: "ArrayMatrix", run: arrayMatrix},
		{name: "NumberKeys", run: numberKeys},
		{name: "PathFacets", run: pathFacets},

		{name: "DecimalMatrix", run: decimalMatrix},
		{name: "ElementPredicates", run: elementPredicates},
		{name: "FilterMatrix", run: filterMatrix},
		{name: "FoldMatrix", run: foldMatrix},
		{name: "GeoMatrix", run: geoMatrix},
		{name: "HasKey", run: hasKey},
		{name: "JSONIntegerEdges_SameRowsOnEveryBackend", run: jsonIntegerEdgesSameRowsOnEveryBackend},
		{name: "LookupSets", run: lookupSets},
		{name: "MembershipMatrix", run: membershipMatrix},
		{name: "PathMatrix", run: pathMatrix},
		{name: "PolicyMatrix", run: policyMatrix},
		{name: "QuotedMatrix", run: quotedMatrix},
		{name: "SetMatrix", run: setMatrix},

		{name: "CoveredByMatchesPostGIS", only: Postgres, run: coveredByMatchesPostGIS},
		{name: "CoveredByMatchesPostGISConstantPolygon", only: Postgres, run: coveredByMatchesPostGISConstantPolygon},
		{name: "DistanceMatchesPostGIS", only: Postgres, run: distanceMatchesPostGIS},

		{name: "ReservedIdentifiers", run: reservedIdentifiers},

		{name: "CompositeKeyPaging", run: compositeKeyPaging},
		{name: "CompositeKeyUpdates", run: compositeKeyUpdates},
		{name: "KeyTypes", run: keyTypes},
		{name: "UUIDKeyRanges", run: uuidKeyRangesCase},

		{name: "KeysetWalk", run: keysetWalk},
		{name: "KeysetWalk_LinguisticCollationWalksByCodePoint", only: Postgres, run: keysetWalkLinguisticCollationWalksByCodePoint},
		{name: "PathWalks", run: pathWalks},

		{name: "GovernedLists", run: governedLists},
		{name: "GovernedLists_Memory", run: governedListsMemory},
		{name: "GovernedMixedTypes", run: governedMixedTypes},
		{name: "ListBodyTransport", run: listBodyTransport},

		{name: "QuoteLiteral_ReadsAsItself", run: quoteLiteralReadsAsItself},

		{name: "ArrayAppendKeepsElementTypes", only: SQLite, run: arrayAppendKeepsElementTypes},
		{name: "BuildPatchSQL_ChangePredicate", run: buildPatchSQLChangePredicate},
		{name: "BuildPatchSQL_FoldsOrderedOps", run: buildPatchSQLFoldsOrderedOps},
		{name: "BuildPatchSQL_Members", run: buildPatchSQLMembers},
		{name: "BuildPatchSQL_Merge", run: buildPatchSQLMerge},
		{name: "BuildPatchSQL_SQLiteWritesAQuoteOrBackslashInAMember", only: SQLite, run: buildPatchSQLSQLiteWritesAQuoteOrBackslashInAMember},
		{name: "Patch_NullableWrapperColumns", run: patchNullableWrapperColumns},
		{name: "PathLabels_ReachTheMemberEncodingJSONNames", run: pathLabelsReachTheMemberEncodingJSONNames},

		{name: "Applier_AgreesWithTheDatabase", run: applierAgreesWithTheDatabase},
		{name: "Applier_FailingMemberOperationAgreesWithTheDatabase", run: applierFailingMemberOperationAgreesWithTheDatabase},
		{name: "Applier_MergesIntoARawMessageMember", run: applierMergesIntoARawMessageMember},

		{name: "RenderRelationOp_Executes", run: renderRelationOpExecutes},

		{name: "SetPlans_SQLiteDecimalKeyIndex", only: SQLite, run: setPlansSQLiteDecimalKeyIndex},

		{name: "PolicyServerFields", run: policyServerFields},
		{name: "RawPolicy", run: rawPolicy},

		{name: "ReadExpr", run: readExpr},

		{name: "CompositeKeyRelations", run: compositeKeyRelations},
		{name: "DeclaredRelations", run: declaredRelations},
		{name: "DeclaredRelations_Facets", run: declaredRelationsFacets},
		{name: "DeclaredRelations_Includes", run: declaredRelationsIncludes},
		{name: "DeclaredRelations_Memberships", run: declaredRelationsMemberships},
		{name: "DeclaredRelations_UnknownPolicy", run: declaredRelationsUnknownPolicy},
		{name: "DeclaredRelations_Writes", run: declaredRelationsWrites},
		{name: "RelationAlgebra", run: relationAlgebra},

		{name: "IntegerTenantKeys", run: integerTenantKeys},
		{name: "TenantKeyGraph", run: tenantKeyGraph},

		{name: "BuildUpdate_AnEmptyPatchIsAScopedRead", run: buildUpdateAnEmptyPatchIsAScopedRead},
		{name: "BuildUpdate_ChangesOnlyTheScopedTargetRows", run: buildUpdateChangesOnlyTheScopedTargetRows},
		{name: "BuildUpdate_ColumnsAndRelationsTogether", run: buildUpdateColumnsAndRelationsTogether},
		{name: "BuildUpdate_Increments", run: buildUpdateIncrements},
		{name: "BuildUpdate_RelationChangeLocksItsOwners", only: Postgres, run: buildUpdateRelationChangeLocksItsOwners},
		{name: "BuildUpdate_RelationsChangeOnlyTheRowsTheOwnerMatched", run: buildUpdateRelationsChangeOnlyTheRowsTheOwnerMatched},
		{name: "BuildUpdate_Stamps", run: buildUpdateStamps},
		{name: "MergePatch_MapKeys", run: mergePatchMapKeys},
		{name: "PathPatches", run: pathPatches},
		{name: "SkipUnchanged_Relations", run: skipUnchangedRelations},
		{name: "SkipUnchanged_WritesOnlyRowsThePatchChanges", run: skipUnchangedWritesOnlyRowsThePatchChanges},
		{name: "TypedMergePatchPreservesSiblings", run: typedMergePatchPreservesSiblings},
		{name: "UpdateBatchDatabase", run: updateBatchDatabase},
		{name: "Update_Members", run: updateMembers},

		{name: "CivilDateSQL", run: civilDateSQL},
		{name: "DecimalSQL", run: decimalSQL},
		{name: "DecimalSQL_PostgresIsExact", only: Postgres, run: decimalSQLPostgresIsExact},
		{name: "Float32FilterAndPatchSQLiteParity", only: SQLite, run: float32FilterAndPatchSQLiteParity},
		{name: "RowContract", run: rowContract},
		{name: "SQLTextValuesSQL", run: sqlTextValuesSQL},
		{name: "Timestamps", run: timestamps},
		{name: "TypedDocumentSQLiteParity", only: SQLite, run: typedDocumentSQLiteParity},
	}
	return cases
}

// RunSQL runs every case of the corpus on engine, each a subtest named for
// the case.
func RunSQL(t *testing.T, engine Engine) {
	t.Helper()
	if !slices.Contains([]Kind{SQLite, Postgres}, engine.Kind) {
		t.Fatalf("corpus: no engine %q", engine.Kind)
	}
	if engine.Open == nil || engine.CallerTx == nil {
		t.Fatalf("corpus: the %s engine has no Open or no CallerTx", engine.Kind)
	}
	for _, c := range sqlCases() {
		if c.only != "" && c.only != engine.Kind {
			continue
		}
		t.Run(c.name, func(t *testing.T) { c.run(t, engine) })
	}
}
