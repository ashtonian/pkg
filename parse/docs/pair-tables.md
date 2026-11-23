# Pair tables compared by key

[Package overview](../README.md) · [Relations](relations.md)

A relation field is virtual (`db:"-"`): it is not a column but a way to filter rows by what they are related to. `relation:"<table>,<owner column>,<target column>[,<owner key>[,<tenant column>]]"` names the table that holds the pairs. Every relation predicate compiles to a correlated `EXISTS` / `NOT EXISTS`, so a row matches once however many related rows it has.

| Shape | How to declare it | Filter | Include | Patch |
|---|---|---|---|---|
| Many-to-many (products ↔ collections) | `relation:"product_collections,product_id,collection_id"` — the junction | yes | yes | yes: membership rows, rendered by `patch.RenderRelationOp` for your transaction |
| One-to-many (a product's reviews) | `relation:"reviews,product_id,id"` — the child table in the junction's place, its key as the target | by child ID, or child existence | yes: the children's IDs | **never** tag `patch`: membership patches would insert or delete child rows |
| Belongs-to (a product's supplier) | the foreign-key column itself, `SupplierID uuid.UUID db:"supplier_id" parse:"filter"` | by the parent's ID | — | the column, like any other |

<!-- output of Example_relations -->
```text
{"collections":"0190f3c2-…-000000000001"}
postgres WHERE EXISTS (SELECT 1 FROM "product_collections" WHERE "product_collections"."product_id" = "products"."id" AND "product_collections"."collection_id" = $1) …
{"collections":{"$nin":["0190f3c2-…-000000000001"]}}
postgres WHERE NOT EXISTS (SELECT 1 FROM "product_collections" WHERE "product_collections"."product_id" = "products"."id" AND "product_collections"."collection_id" = ANY($1)) …
{"reviews":{"$null":false}}
postgres WHERE EXISTS (SELECT 1 FROM "reviews" WHERE "reviews"."product_id" = "products"."id") …
{"supplier_id":"0190f3c2-…-0000000000aa"}
postgres WHERE "products"."supplier_id" = $1 …
```

The four filters select the products in a collection, the products not in it, the products with at least one review, and a supplier's products.

`$eq x` means some related row is x, `$ne x` that none is, `$null: true` that there is none. `include=collections` returns each owner's related IDs in the page's `_relations`, distinct and capped per owner in SQL (`filter.BuildIncludeQuery`, default 100). A relation facet counts distinct owners per target.

A tag relation compares related IDs only; filter by a related row's attributes through a declared relation. It costs 8 when the field is tagged `indexed:"true"` and 80 when not, against a default `MaxCost` of 50 (`core.WithMaxCost`); tag it indexed when the pair table is indexed on the owner column, as it should be. Outside SQL its key comparisons resolve as memberships of the pair table (`ResolveMemberships`); other operators on it are SQL-only.

A junction with a tenant column (`relation:"…,id,tenant_id"`) binds the caller's tenant in every statement and refuses to build without one (`filter.RelationScope.Tenant`, `RenderRelationOpOptions.TenantValue`), or, for rows of several tenants, compares it with the owner row's tenant column (`RelationScope.RowTenant`, as a [governed list](relations.md#governed-lists) does); `RelationScope.Unscoped` is the explicit opt-out for platform tooling.

## Declare a pair table

The tag grammar is `relation:"table,owner_column,target_column[,primary_key[,tenant_column]]"`. The owner primary key defaults to `id`. Every component must be an identifier. The field is a virtual slice of target IDs, usually `db:"-"`; the IDs and the owner key are strings, UUIDs or integers; its `parse` capabilities independently allow filtering, including, faceting, and patching.

For a virtual relation hidden from the base row JSON, add `relation_name:"tag_ids"` beside `json:"-"` to preserve its external capability name. This avoids deriving `TagIDs` as an unintended snake-case spelling. It only affects the hidden-relation fallback, does not change visible JSON names, and does not grant additional filter/include/facet/patch capabilities. Duplicate external names are rejected during schema generation.

## Load related IDs

1. Validate the requested names with `filter.ParseIncludes(schema, parameter)`.
2. Execute the base page query and collect its owner IDs.
3. For each requested relation, call `filter.BuildIncludeQuery(field, dialect, ownerIDs, options...)`. The owner IDs bind as one set, typed as the relation's owner correlation field (`FieldSchema.RelatedBy`, which a resource sets), so the statement's text is the same for every page; a field without one is an error. Scope it with `WithIncludeRelationScope`: a `Tenant` for tenant-scoped junctions, or a `RowTenant` with `WithIncludeOwner(ownerSchema)` when the owners span tenants.
4. Execute its SQL and read the rows with the query's `Scan`, which groups target IDs by owner and lists the owners whose IDs were cut at the cap in `Truncated`.
5. Attach the result to `filter.Page[T].Relations`, whose JSON name is `_relations`.

Includes return IDs, not nested child objects. The default cap is 100 distinct targets per owner; change it with `filter.WithMaxIncludePerRelation`. The query reads one more than the cap, so `Scan` knows a cut list from a complete one; put `Truncated` in the page's `Truncated[field]` (`_truncated` on the wire). `Resource.IncludeQueries` and `parsepgx.QueryPage` do this for you. The caller owns execution and transaction consistency.

## Update membership

`patch.BuildPatchSQL` separates junction operations into `RelationOps`. For each operation, `patch.RenderRelationOp` produces statements for the selected owner IDs, owners and values each bound as one array. Without a target table there are no target rows to select, so PostgreSQL's insert reads the owners and values as rows of the pair table's own type (`jsonb_populate_recordset(NULL::"<pair table>", $n)`), which types each key as its column; each record names its column exactly as the relation declares it, the spelling every quoted statement reads. Run owner authorization/selection and every resulting statement in one transaction. Membership `replace` removes targets not in the new set and adds missing ones; `add` and `remove` modify the selected memberships.

Bind the trusted tenant with `patch.RenderRelationOpOptions.TenantValue`. Serialize conflicting replacements of the same owner (for example by locking the owner row in PostgreSQL) so concurrent replacements do not combine memberships. The package renders SQL; it does not open a transaction or acquire locks.

See [declared relations](relations.md), [query options](queries.md#configuration), [patch compilation](patches.md#sql-compilation), and [integration responsibilities](integrations.md).
