//go:build integration

// Package corpus is the database-backed suite parse's providers run: every
// case runs parse over a fresh database, through resources on a provider
// or through filter's and patch's builders on the database itself, and
// asserts the rows that come back, the rows stored and the statements
// sent. parsesql's tests run it on SQLite, parsepgx's on PostgreSQL and
// parsememory's on the memory provider (Run), so each provider and dialect
// is held to the same cases. A case that asks a SQL database for what the
// memory provider does not hold (statements of its own, the SQL builders,
// constraints, query plans) names why (sqlCase.sqlOnly), and the Memory
// engine skips it. The fixtures the root package's and filter's own tests
// also use live in internal/parsetest.
package corpus

import (
	"slices"
	"testing"
)

// sqlCase is one case of the corpus: a subtest of Run.
type sqlCase struct {
	name string
	// only is the one kind of engine the case runs on, for a feature of
	// that engine alone; empty runs it on every engine.
	only Kind
	// sqlOnly is why the case needs a SQL database, which the Memory
	// engine skips it for; empty runs it there too.
	sqlOnly string
	run     func(t *testing.T, engine Engine)
}

// sqlCases are the corpus's cases, grouped by the file that holds them
// and sorted by name within it.
func sqlCases() []sqlCase {
	cases := []sqlCase{
		{name: "BuildBaseConstraints_FailClosedWithoutATenant", run: buildBaseConstraintsFailClosedWithoutATenant},
		{name: "ComposeConstraints", run: composeConstraints},
		{name: "KeyConstraint_EmptySetAdmitsNoRow", run: keyConstraintEmptySetAdmitsNoRow},
		{name: "KeyConstraint_SelectsWholeKeys", run: keyConstraintSelectsWholeKeys},
		{name: "PolicyStoredValues", run: policyStoredValues},

		{name: "ExecutorContract", run: executorContract},

		{name: "ArrayMatrix", run: arrayMatrix},
		{name: "BuildFacetQuery_RelationFacet_KeysByCodePoint", run: buildFacetQueryRelationFacetKeysByCodePoint},
		{name: "BuildFacetQuery_RelationFacet_NumbersTieByValue", run: buildFacetQueryRelationFacetNumbersTieByValue},
		{name: "BuildFacetQuery_RelationFacet_TargetKeyOfSeveralColumns", run: buildFacetQueryRelationFacetTargetKeyOfSeveralColumns},
		{name: "BuildFacetQuery_RelationFacets_OwnKeyEach", run: buildFacetQueryRelationFacetsOwnKeyEach},
		{name: "BuildFacetQuery_RoundTrip_BooleanKeysAreJSONLiterals", run: buildFacetQueryRoundTripBooleanKeysAreJSONLiterals},
		{name: "BuildFacetQuery_RoundTrip_FacetFrom", sqlOnly: "facets the rows of a caller's FROM clause (filter.WithFacetFrom)", run: buildFacetQueryRoundTripFacetFrom},
		{name: "BuildFacetQuery_RoundTrip_ListElements", run: buildFacetQueryRoundTripListElements},
		{name: "BuildFacetQuery_RoundTrip_NumbersTieByValue", run: buildFacetQueryRoundTripNumbersTieByValue},
		{name: "BuildFacetQuery_RoundTrip_Order", run: buildFacetQueryRoundTripOrder},
		{name: "BuildFacetQuery_RoundTrip_PerFacetLimit", run: buildFacetQueryRoundTripPerFacetLimit},
		{name: "BuildFacetQuery_RoundTrip_TenantScoped", run: buildFacetQueryRoundTripTenantScoped},
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
		{name: "NullOperands", run: nullOperands},
		{name: "PathMatrix", run: pathMatrix},
		{name: "PolicyMatrix", run: policyMatrix},
		{name: "QuotedMatrix", run: quotedMatrix},
		{name: "SetMatrix", run: setMatrix},

		{name: "CoveredByMatchesPostGIS", only: Postgres, run: coveredByMatchesPostGIS},
		{name: "CoveredByMatchesPostGISConstantPolygon", only: Postgres, run: coveredByMatchesPostGISConstantPolygon},
		{name: "DistanceMatchesPostGIS", only: Postgres, run: distanceMatchesPostGIS},

		{name: "ReservedIdentifiers", run: reservedIdentifiers},

		{name: "BuildIncludeQuery_TextKeysUseCodePointOrder", run: buildIncludeQueryTextKeysUseCodePointOrder},
		{name: "RelationE2E_Facet_TenantScoped", run: relationE2EFacetTenantScoped},
		{name: "RelationE2E_Filter_CrossTenantBlocked", run: relationE2EFilterCrossTenantBlocked},
		{name: "RelationE2E_Filter_TenantScopedJunction", run: relationE2EFilterTenantScopedJunction},
		{name: "RelationE2E_Include_CapPerOwner", run: relationE2EIncludeCapPerOwner},
		{name: "RelationE2E_Include_TenantScoped", run: relationE2EIncludeTenantScoped},
		{name: "RelationE2E_Patch_Replace_Add_Remove_Atomic", sqlOnly: "runs the statements patch.RenderRelationOp writes", run: relationE2EPatchReplaceAddRemoveAtomic},
		{name: "Relation_TypedEdgesCountOnce", run: relationTypedEdgesCountOnce},

		{name: "CompositeKeyPaging", run: compositeKeyPaging},
		{name: "CompositeKeyUpdates", run: compositeKeyUpdates},
		{name: "KeyTypes", run: keyTypes},
		{name: "UUIDKeyRanges", run: uuidKeyRangesCase},
		{name: "ValuerKeys", run: valuerKeys},

		{name: "Integration_CursorWalkNoLossNoDup", run: integrationCursorWalkNoLossNoDup},
		{name: "KeysetRender", sqlOnly: "orders and seeks a statement of its own by a computed key (filter.OrderByKeys, filter.SeekPredicate)", run: keysetRender},
		// NOCASE and an expression's own COLLATE are SQLite's; PostgreSQL's
		// counterpart is KeysetWalk_LinguisticCollationWalksByCodePoint.
		{name: "KeysetSeek_SQLiteCollatedExpressionWalksByCodePoint", only: SQLite, run: keysetSeekSQLiteCollatedExpressionWalksByCodePoint},
		{name: "KeysetSeek_SQLiteNoCaseKeyWalksByCodePoint", only: SQLite, run: keysetSeekSQLiteNoCaseKeyWalksByCodePoint},
		{name: "KeysetWalk", run: keysetWalk},
		{name: "KeysetWalk_LinguisticCollationWalksByCodePoint", only: Postgres, run: keysetWalkLinguisticCollationWalksByCodePoint},
		{name: "ListQuery_CompositeKeyBreaksTies", run: listQueryCompositeKeyBreaksTies},
		{name: "ListQuery_KeyBreaksTies", run: listQueryKeyBreaksTies},
		{name: "PathWalks", run: pathWalks},

		{name: "GovernedLists", run: governedLists},
		{name: "GovernedLists_Memory", run: governedListsMemory},
		{name: "GovernedMixedTypes", run: governedMixedTypes},
		{name: "ListBodyTransport", run: listBodyTransport},
		{name: "ListOffsetDoesNotOffsetStandaloneQueries", sqlOnly: "numbers the parameters of a list's statements", run: listOffsetDoesNotOffsetStandaloneQueries},

		{name: "QuoteLiteral_ReadsAsItself", sqlOnly: "selects each literal a SQL dialect quotes (Dialect.QuoteLiteral)", run: quoteLiteralReadsAsItself},

		{name: "ArrayAppendKeepsElementTypes", only: SQLite, run: arrayAppendKeepsElementTypes},
		{name: "BuildPatchSQL_ChangePredicate", sqlOnly: "runs the statements patch.BuildPatchSQL writes", run: buildPatchSQLChangePredicate},
		{name: "BuildPatchSQL_FoldsOrderedOps", sqlOnly: "runs the statements patch.BuildPatchSQL writes", run: buildPatchSQLFoldsOrderedOps},
		{name: "BuildPatchSQL_Members", sqlOnly: "runs the statements patch.BuildPatchSQL writes", run: buildPatchSQLMembers},
		{name: "BuildPatchSQL_Merge", sqlOnly: "runs the statements patch.BuildPatchSQL writes", run: buildPatchSQLMerge},
		{name: "BuildPatchSQL_SQLiteWritesAQuoteOrBackslashInAMember", only: SQLite, run: buildPatchSQLSQLiteWritesAQuoteOrBackslashInAMember},
		{name: "Patch_NullableWrapperColumns", run: patchNullableWrapperColumns},
		{name: "PathLabels_ReachTheMemberEncodingJSONNames", sqlOnly: "escapes a member's label into the SQL a filter and patch.BuildPatchSQL write", run: pathLabelsReachTheMemberEncodingJSONNames},

		{name: "Applier_AgreesWithTheDatabase", sqlOnly: "holds the Applier to the statements patch.BuildPatchSQL writes", run: applierAgreesWithTheDatabase},
		{name: "Applier_FailingMemberOperationAgreesWithTheDatabase", sqlOnly: "holds the Applier to the precondition SQL patch.RenderPreconditions writes", run: applierFailingMemberOperationAgreesWithTheDatabase},
		{name: "Applier_MergesIntoARawMessageMember", sqlOnly: "holds the Applier to the statements patch.BuildPatchSQL writes", run: applierMergesIntoARawMessageMember},

		{name: "ComposedPatch_ApplyAgreesWithTheDatabase", sqlOnly: "holds ComposedPatch.Apply, which the memory engine's updates run, to the update a SQL database runs", run: composedPatchApplyAgreesWithTheDatabase},

		{name: "RenderRelationOp_Executes", sqlOnly: "runs the statements patch.RenderRelationOp writes", run: renderRelationOpExecutes},

		{name: "CanonicalOrder", run: canonicalOrder},
		{name: "CanonicalOrder_SQLiteTimeColumns", only: SQLite, run: canonicalOrderSQLiteTimeColumns},

		// SQLite's query plans (EXPLAIN QUERY PLAN).
		{name: "KeysetSeek_SQLiteDecimalKeySearchesItsIndex", only: SQLite, run: keysetSeekSQLiteDecimalKeySearchesItsIndex},
		{name: "KeysetSeek_SQLiteSearchesTheSortIndex", only: SQLite, run: keysetSeekSQLiteSearchesTheSortIndex},
		{name: "RequestViewPaths", run: requestViewPaths},
		{name: "WidgetLists", run: widgetLists},

		{name: "SetPlans_SQLiteDecimalKeyIndex", only: SQLite, run: setPlansSQLiteDecimalKeyIndex},

		{name: "PolicyServerFields", run: policyServerFields},
		{name: "RawPolicy", run: rawPolicy},

		{name: "Integration_Facets", run: integrationFacets},
		{name: "Integration_InOperator", run: integrationInOperator},
		{name: "Integration_ListQuery_PolicyConstraints", run: integrationListQueryPolicyConstraints},
		{name: "Integration_ListQuery_SortAndLimit", run: integrationListQuerySortAndLimit},
		{name: "Integration_ListQuery_WithCount", run: integrationListQueryWithCount},
		{name: "Integration_MultiCondition", run: integrationMultiCondition},
		{name: "Integration_NotOperator", run: integrationNotOperator},
		{name: "Integration_OrOperator", run: integrationOrOperator},
		{name: "Integration_SimpleEq", run: integrationSimpleEq},
		{name: "Integration_WithStartParam", sqlOnly: "numbers a WHERE after an UPDATE's SET parameters (filter.WithStartParam)", run: integrationWithStartParam},

		{name: "ReadExpr", run: readExpr},
		{name: "ReadExpr_RelationIncludesCompileTheTargetsPolicy", run: readExprRelationIncludesCompileTheTargetsPolicy},
		{name: "ReadExpr_UpdateRereadsAfterRelationChanges", run: readExprUpdateRereadsAfterRelationChanges},

		{name: "CompositeKeyRelations", run: compositeKeyRelations},
		{name: "DeclaredRelations", run: declaredRelations},
		{name: "DeclaredRelations_Facets", run: declaredRelationsFacets},
		{name: "DeclaredRelations_Includes", run: declaredRelationsIncludes},
		{name: "DeclaredRelations_Memberships", sqlOnly: "answers memberships with the subqueries SQL renders for them (sqlMembershipLookup)", run: declaredRelationsMemberships},
		{name: "DeclaredRelations_UnknownPolicy", run: declaredRelationsUnknownPolicy},
		{name: "DeclaredRelations_Writes", run: declaredRelationsWrites},
		{name: "RelationAlgebra", run: relationAlgebra},

		{name: "List_NamesAScanFailureOnce", run: listNamesAScanFailureOnce},
		{name: "RowScanner_EmbeddedStructs", run: rowScannerEmbeddedStructs},
		{name: "RowScanner_RefusesWhatTheRowCannotHold", run: rowScannerRefusesWhatTheRowCannotHold},
		{name: "RowScanner_Storage", run: rowScannerStorage},

		{name: "IntegerTenantKeys", run: integerTenantKeys},
		{name: "IntegerTenantKeys_PairsHoldTheBoundKey", run: integerTenantKeysPairsHoldTheBoundKey},
		{name: "TenantKeyGraph", run: tenantKeyGraph},

		{name: "In_TransactionsOfItsProvider", run: inTransactionsOfItsProvider},

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
		{name: "SkipUnchanged_RowKeys", run: skipUnchangedRowKeys},
		{name: "SkipUnchanged_WritesOnlyRowsThePatchChanges", run: skipUnchangedWritesOnlyRowsThePatchChanges},
		{name: "TypedMergePatchPreservesSiblings", run: typedMergePatchPreservesSiblings},
		{name: "UpdateBatchDatabase", run: updateBatchDatabase},
		{name: "UpdateBatch_CanonicalKeys", run: updateBatchCanonicalKeys},
		{name: "UpdateBatch_MissingTargetNamesItsResourceOnce", run: updateBatchMissingTargetNamesItsResourceOnce},
		{name: "UpdateClock_MembershipAtThePlansInstant", run: updateClockMembershipAtThePlansInstant},
		{name: "Update_Members", run: updateMembers},

		{name: "CivilDateSQL", run: civilDateSQL},
		{name: "DecimalSQL", run: decimalSQL},
		{name: "DecimalSQL_PostgresIsExact", only: Postgres, run: decimalSQLPostgresIsExact},
		{name: "ExactIntegers", run: exactIntegers},
		{name: "Float32FilterAndPatchSQLiteParity", only: SQLite, run: float32FilterAndPatchSQLiteParity},
		{name: "RowContract", run: rowContract},
		{name: "SQLTextValuesSQL", run: sqlTextValuesSQL},
		{name: "Timestamps", run: timestamps},
		{name: "TypedDocumentSQLiteParity", only: SQLite, run: typedDocumentSQLiteParity},

		{name: "CodedWalks", run: codedWalks},
		{name: "NonFiniteDecimal_SQLiteReadsThemAsNull", only: SQLite, run: nonFiniteDecimalSQLiteReadsThemAsNull},
		// SQLite stores a REAL NaN as NULL, PostgreSQL holds it after
		// +Infinity, so each engine walks its own order. NonFiniteFloatFacet
		// and NonFiniteFloatFilter are SQLite's REAL column under the helpers
		// NonFiniteFilterAndFacet runs on PostgreSQL's float8 and numeric.
		{name: "NonFiniteFilterAndFacet", only: Postgres, run: nonFiniteFilterAndFacet},
		{name: "NonFiniteFloatFacet", only: SQLite, run: nonFiniteFloatFacet},
		{name: "NonFiniteFloatFilter", only: SQLite, run: nonFiniteFloatFilter},
		{name: "NonFiniteFloatSeek_SQLiteNaNIsNull", only: SQLite, run: nonFiniteFloatSeekSQLiteNaNIsNull},
		{name: "NonFiniteFloatWalk", only: SQLite, run: nonFiniteFloatWalk},
		{name: "NonFiniteWalk", only: Postgres, run: nonFiniteWalk},
		{name: "NullableWrappers", run: nullableWrappers},
		{name: "PathNumberFacets", run: pathNumberFacets},
		{name: "PathNumberFilters", run: pathNumberFilters},
		{name: "PathNumberWalk", run: pathNumberWalk},
		{name: "PathNumberWalk_PostgresResumesAFloat64TokenFromItsRoundedValue", only: Postgres, run: pathNumberWalkPostgresResumesAFloat64TokenFromItsRoundedValue},

		{name: "FacetExternalName", run: facetExternalName},
		{name: "QuotedRelationKeys", run: quotedRelationKeys},
		{name: "SDKWire", run: sdkWireFixture},
	}
	return cases
}

// Run runs every case of the corpus on engine, each a subtest named for
// the case; on the Memory engine a case that needs SQL is skipped, naming
// why.
func Run(t *testing.T, engine Engine) {
	t.Helper()
	switch {
	case !slices.Contains([]Kind{SQLite, Postgres, Memory}, engine.Kind):
		t.Fatalf("corpus: no engine %q", engine.Kind)
	case engine.Kind == Memory && engine.OpenStore == nil:
		t.Fatalf("corpus: the memory engine has no OpenStore")
	case engine.Kind != Memory && (engine.Open == nil || engine.CallerTx == nil):
		t.Fatalf("corpus: the %s engine has no Open or no CallerTx", engine.Kind)
	}
	for _, c := range sqlCases() {
		if c.only != "" && c.only != engine.Kind {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			if engine.Kind == Memory && c.sqlOnly != "" {
				t.Skip("needs SQL: " + c.sqlOnly)
			}
			c.run(t, engine)
		})
	}
}
