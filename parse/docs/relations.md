# Relations

[Package overview](../README.md)

A relation lets a request filter rows by the rows they are related to. Declare it on the owner resource with `Resource.Relate` — a has-many, a belongs-to or a many-to-many to another `Resource` — and requests filter by the related rows with the target's own filter. The older `relation:"…"` struct tag declares a pair table compared by key alone. Every relation predicate compiles to one correlated `EXISTS` / `NOT EXISTS`, so a row matches once however many related rows it has.

## Declare relations

Relate both resources after constructing them, so they can name each other, and before they serve requests:

```go
devices := parse.MustResource[Device]("devices", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
commands := parse.MustResource[Command]("commands", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
types := parse.MustResource[DeviceType]("device_types", parse.TenantColumn("tenant_id"))
tags := parse.MustResource[Tag]("tags", parse.TenantColumn("tenant_id"))

devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))     // commands.device_id = devices.id
devices.MustRelate("type", parse.BelongsTo(types, "type_id", parse.PolicyScoped()))      // device_types.id = devices.type_id
devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: "device_id", Target: "tag_id", Tenant: "tenant_id"}, parse.Indexed()))
commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))
```

| Declaration | Relates | Correlation |
|---|---|---|
| `HasMany(target, fk)` | an owner to the target rows whose `fk` holds its key | `target.fk = owner.<key>`; `References(col)` names another owner column |
| `BelongsTo(target, fk)` | an owner to the target row its `fk` names | `target.<key> = owner.fk`; `References(col)` names another target column |
| `ManyToMany(target, junction)` | owners and targets through a pair table | `junction.Source = owner.<key>`, `target.<key> = junction.Target` |

`Relate` checks every column against both resources' types and returns an error; `MustRelate` panics. A relation may lead back to its own table (an asset's parent); the query aliases it so it does not shadow the owner.

| Option | Effect |
|---|---|
| `Indexed()` | An index leads with the correlation column (the foreign key, or the pair table's owner column): the relation costs 8 instead of 80 against `MaxCost` (default 50). A belongs-to correlates on the target's key and is always indexed. |
| `PolicyScoped()` | The related rows are those the server's policy for the target table admits (`Scope.Related[<table>]`), not the request's tenant: rows another tenant shares, such as platform device types. A list using the relation without that policy fails with `filter.ErrRelatedPolicyRequired`. |
| `References(column)` | The column a foreign key holds when it is not the referenced resource's key |
| `Describe(label, description)` | The label and description schema discovery serves for the relation field, as the `label` and `description` tags do for a column |

Related rows are live rows only (the target's `DeletedColumn`) and, unless policy-scoped, rows of the request's tenant (the target's `TenantColumn`). A pair table's `Tenant` column is bound to the request's tenant.

## Filter by related rows

| Request | Means |
|---|---|
| `{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}` | some visible command is pending with more than two attempts — one command satisfies every predicate inside one `$some` |
| `{"commands":{"$none":{"status":"failed"}}}` | no visible command failed; devices without commands match |
| `{"type":{"$is":{"name":{"$like":"pump%"}}}}` | the device's type is visible and matches; `$is_not` is its negation, so devices without a visible type match it |
| `{"commands":"c1"}`, `{"commands":{"$in":["c1","c2"]}}` | some visible command has that key; `$ne` / `$nin` are `$none` |
| `{"commands":{"$null":true}}` | no visible command (`$none: {}`); `$null: false` is `$some: {}` |

`$some` / `$none` apply to has-many and many-to-many relations, `$is` / `$is_not` to belongs-to. A NULL foreign key is tested with the foreign-key column's own `$null` (`{"type_id":{"$null":true}}`), which says nothing about whether the type is visible. The target's own relations may be used inside the target's filter, two levels deep (`devices → commands → receipts`, E2074 beyond); a filter holds at most 8 relation predicates (E2089 beyond). A relation predicate costs the relation (8 indexed, 80 not) plus its nested filter's cost. Flat parameters (`?commands=c1`) and policy constraints that name a relation take the same path.

```text
{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}
postgres WHERE (EXISTS (SELECT 1 FROM commands WHERE commands.device_id = devices.id AND commands.tenant_id = $1 AND (commands.attempts > $2 AND commands.status = $3)) AND devices.tenant_id = $4) … [t1 2 pending t1]

{"commands":{"$none":{"status":"failed"}}}
postgres WHERE (NOT EXISTS (SELECT 1 FROM commands WHERE commands.device_id = devices.id AND commands.tenant_id = $1 AND commands.status = $2) AND devices.tenant_id = $3) … [t1 failed t1]
```

The output above is `ExampleResource_Relate`, checked by the unit lane. A many-to-many with a target filter joins the target inside the subquery: `EXISTS (SELECT 1 FROM device_tags rel1__ JOIN tags ON tags.id = rel1__.tag_id WHERE rel1__.device_id = devices.id AND rel1__.tenant_id = $1 AND tags.tenant_id = $2 AND tags.label = $3)`. A target field computed from something other than its table's column cannot be used through a relation (the build fails); a relation without a target resource takes no target filter (E2083).

### Outside SQL

A backend that holds rows in memory evaluates relation predicates as memberships: `filter.ResolveMemberships(ctx, f, lookup, filter.ResolveTenant(tenant), filter.ResolveRelatedPolicies(related))` replaces each one, before any row is read, with a comparison on the owner's correlation field (`FieldSchema.RelatedBy`, the key or a belongs-to's foreign key): `$some` is the field among the related rows' correlation values, `$none` the field outside them or NULL, exactly as `NOT EXISTS` admits a NULL foreign key. The related rows are a `filter.Lookup` over the target (or pair) table — its live rows of the tenant, the projected column never NULL — and, for a target filter, `Lookup.Filter`, the target's filter with its policy composed in. A many-to-many is the pair rows whose target is in the target's lookup (`WhereMember`); depth-two relations resolve inside the target's filter first.

Lookups with a `Filter` go only to a `filter.RelationLookup`, whose `LookupFilteredValues` evaluates the filter with the store's own evaluator; a store that is only a `MembershipLookup` refuses those requests (E2083) rather than ignore the filter and over-match. Without a lookup, or without a tenant for a tenant-scoped relation, the request is refused too. `relation_declared_cases_test.go` answers every case both ways — `EXISTS` and resolved memberships — on SQLite and PostgreSQL and requires the same rows.

### Include related keys

Declare `Includable()` on a has-many or many-to-many and a list may ask for its related keys: `include=commands,tags` puts each page row's visible related keys in the page's `_relations` — a has-many's live children in the tenant, a many-to-many's pairs whose target is live and in the tenant (or, policy-scoped, admitted by `Scope.Related`). A list includes at most `filter.MaxIncludes` (3) relations (E2083 beyond). A belongs-to's key is the row's own foreign-key column, so it has nothing to include.

### Facet by related keys

Declare `Facetable()` on a has-many or many-to-many and `facets=tags` counts the matched rows per visible related key — distinct owners per key, the related rows scoped as includes and filters are. A belongs-to facets through its foreign-key column instead.

### Change membership

Declare `Writable()` on a many-to-many and a patch changes its membership with target keys: a merge patch `{"tags": ["red", "blue"]}` replaces the set, a JSON Patch `add` / `remove` on `/tags` with a list adds or removes keys. `BuildUpdate` returns the owner statement and `RelationStatements` the membership statements for the keys it returned (`parsepgx.Update` runs both in one transaction). Pairs are made only with targets the request may relate — live, in the tenant, or admitted by the target's policy — by selecting the owner and target rows rather than binding raw keys, and a replace or remove reaches only pairs to such targets, so pairs the request cannot see stay:

```sql
INSERT INTO device_tags (device_id, tag_id, tenant_id)
SELECT owner__.id, target__.id, owner__.tenant_id FROM devices owner__, tags target__
WHERE owner__.id IN ($1) AND owner__.tenant_id = $2 AND target__.id IN ($3, $4)
  AND target__.tenant_id = $5 AND target__.deleted_at IS NULL ON CONFLICT DO NOTHING
```

A has-many is never written through its parent (that would insert or delete child rows); a belongs-to is written through its foreign-key column like any other column. A writable pair table's tenant comes from the owner or the target.

## Pair tables compared by key

A relation field is virtual (`db:"-"`): it is not a column but a way to filter rows by what they are related to. `relation:"<table>,<owner column>,<target column>[,<owner key>[,<tenant column>]]"` names the table that holds the pairs. Every relation predicate compiles to a correlated `EXISTS` / `NOT EXISTS`, so a row matches once however many related rows it has.

| Shape | How to declare it | Filter | Include | Patch |
|---|---|---|---|---|
| Many-to-many (products ↔ collections) | `relation:"product_collections,product_id,collection_id"` — the junction | yes | yes | yes: membership rows, rendered by `patch.RenderRelationOp` for your transaction |
| One-to-many (a product's reviews) | `relation:"reviews,product_id,id"` — the child table in the junction's place, its key as the target | by child ID, or child existence | yes: the children's IDs | **never** tag `patch`: membership patches would insert or delete child rows |
| Belongs-to (a product's supplier) | the foreign-key column itself, `SupplierID uuid.UUID db:"supplier_id" parse:"filter"` | by the parent's ID | — | the column, like any other |

```text
{"collections":"0190f3c2-…-000000000001"}                  products in a collection
postgres WHERE EXISTS (SELECT 1 FROM product_collections rel__ WHERE rel__.product_id = products.id AND rel__.collection_id = $1)

{"collections":{"$nin":["0190f3c2-…-000000000001"]}}       products not in it
postgres WHERE NOT EXISTS (SELECT 1 FROM product_collections rel__ WHERE rel__.product_id = products.id AND rel__.collection_id IN ($1))

{"reviews":{"$null":false}}                                products with at least one review
postgres WHERE EXISTS (SELECT 1 FROM reviews rel__ WHERE rel__.product_id = products.id)

{"supplier_id":"0190f3c2-…-0000000000aa"}                  a supplier's products
postgres WHERE products.supplier_id = $1
```

`$eq x` means some related row is x, `$ne x` that none is, `$null: true` that there is none. `include=collections` returns each owner's related IDs in the page's `_relations`, distinct and capped per owner in SQL (`filter.BuildIncludeQuery`, default 100). A relation facet counts distinct owners per target.

A tag relation compares related IDs only; filter by a related row's attributes through a declared relation. It costs 8 when the field is tagged `indexed:"true"` and 80 when not, against a default `MaxCost` of 50 (`core.WithMaxCost`); tag it indexed when the pair table is indexed on the owner column, as it should be. Outside SQL its key comparisons resolve as memberships of the pair table (`ResolveMemberships`); other operators on it are SQL-only.

A junction with a tenant column (`relation:"…,id,tenant_id"`) binds the caller's tenant in every statement and refuses to build without one (`filter.ListTenant`, `WithIncludeTenant`, `RenderRelationOpOptions.TenantValue`); `ListUnscopedRelations` is the explicit opt-out for platform tooling.

## Declare the relation

The tag grammar is `relation:"table,owner_column,target_column[,primary_key[,tenant_column]]"`. The owner primary key defaults to `id`. Every component must be an identifier. The field is a virtual slice of target IDs, usually `db:"-"`; its `parse` capabilities independently allow filtering, including, faceting, and patching.

For a virtual relation hidden from the base row JSON, add `relation_name:"tag_ids"` beside `json:"-"` to preserve its external capability name. This avoids deriving `TagIDs` as an unintended snake-case spelling. It only affects the hidden-relation fallback, does not change visible JSON names, and does not grant additional filter/include/facet/patch capabilities. Duplicate external names are rejected during schema generation.

## Load related IDs

1. Validate the requested names with `filter.ParseIncludes(schema, parameter)`.
2. Execute the base page query and collect its owner IDs.
3. For each requested relation, call `filter.BuildIncludeQuery(field, dialect, ownerIDs, options...)`. Bind `WithIncludeTenant` for tenant-scoped junctions.
4. Execute its SQL and read the rows with the query's `Scan`, which groups target IDs by owner and lists the owners whose IDs were cut at the cap in `Truncated`.
5. Attach the result to `filter.Page[T].Relations`, whose JSON name is `_relations`.

Includes return IDs, not nested child objects. The default cap is 100 distinct targets per owner; change it with `filter.WithMaxIncludePerRelation`. The query reads one more than the cap, so `Scan` knows a cut list from a complete one; put `Truncated` in the page's `Truncated[field]` (`_truncated` on the wire). `Resource.IncludeQueries` and `parsepgx.QueryPage` do this for you. The caller owns execution and transaction consistency.

## Update membership

`patch.BuildPatchSQL` separates junction operations into `RelationOps`. For each operation, `patch.RenderRelationOp` produces statements for the selected owner IDs. Run owner authorization/selection and every resulting statement in one transaction. Membership `replace` removes targets not in the new set and adds missing ones; `add` and `remove` modify the selected memberships.

Bind the trusted tenant with `patch.RenderRelationOpOptions.TenantValue`. Serialize conflicting replacements of the same owner (for example by locking the owner row in PostgreSQL) so concurrent replacements do not combine memberships. The package renders SQL; it does not open a transaction or acquire locks.

See [query options](queries.md#configuration), [patch compilation](patches.md#sql-compilation), and [integration responsibilities](integrations.md).
