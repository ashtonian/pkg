# Relations

[Package overview](../README.md)

A relation lets a request filter rows by the rows they are related to. Declare it on the owner resource with `Resource.Relate` — a has-many, a belongs-to or a many-to-many to another `Resource` — and requests filter by the related rows with the target's own filter. A `relation:"…"` struct tag declares a [pair table](pair-tables.md) instead: membership compared by the target's key alone, for a target that is not a `Resource` or a request that only matches related keys. Every relation predicate compiles to one correlated `EXISTS` / `NOT EXISTS`, so a row matches once however many related rows it has.

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
| `ManyToMany(target, junction)` | owners and targets through a pair table, a `Junction` (`Through(table, source, target)` builds one without a tenant column) | `junction.Source = owner.<key>`, `target.<key> = junction.Target` |

`Relate` checks every column against both resources' types and returns an error; `MustRelate` panics. A relation's name must be a name no server policy already resolves on the owner: a request field, a system field, or any [policy field](queries.md#policy-fields), the owner's columns among them. Otherwise the relation would change what a policy on that name constrains. A relation may lead back to its own table (an asset's parent); the query aliases it so it does not shadow the owner.

| Option | Effect |
|---|---|
| `Indexed()` | An index leads with the correlation column (the foreign key, or the pair table's owner column): the relation costs 8 instead of 80 against `MaxCost` (default 50). A belongs-to correlates on the target's key and is always indexed. |
| `PolicyScoped()` | The related rows are those the server's policy for the target table admits (`Scope.Related[<table>]`), not the request's tenant: rows another tenant shares, such as platform device types. A list using the relation without that policy fails with `filter.ErrRelatedPolicyRequired`. |
| `References(column)` | The column a foreign key holds when it is not the referenced resource's key |
| `Includable()` | A list may include the related rows' keys (`include=<name>`): a has-many's children, a many-to-many's targets, as visible to the request ([include related keys](#include-related-keys)). A belongs-to's key is the owner's own foreign-key column |
| `Facetable()` | A list may count its rows per related key (`facets=<name>`): distinct owners per visible child or target ([facet by related keys](#facet-by-related-keys)). A belongs-to facets through its foreign-key column |
| `Writable()` | A patch may change a many-to-many's membership with target keys ([change membership](#change-membership)). The pair table must hold nothing but the pairs and their tenant |
| `Describe(label, description)` | The label and description schema discovery serves for the relation field, as the `label` and `description` tags do for a column |

Related rows are live rows only (the target's `DeletedColumn`) and, unless policy-scoped, rows of the request's tenant (the target's `TenantColumn`). A pair table's `Tenant` column is bound to the request's tenant, as one value with every other tenant column the request reaches ([Tenant key](#tenant-key)). A governed list's rows span tenants, and each row's related rows are its own tenant's ([Governed lists](#governed-lists)).

### Tenant key

One request has one tenant, and every statement binds it as one value: `Scope.Tenant` is checked and normalized once, when the statement is built, against every tenant column the resource reaches. That is its own `TenantColumn`, each relation target's (unless the relation is `PolicyScoped`), and, at any depth, the tenant columns of the targets' own relations, since a relation predicate nests its target's filter with that target's relations (`{"commands":{"$some":{"logs":{"$some":{…}}}}}`). A policy-scoped target's own tenant column is its policy's, but its relations still bind the tenant.

| Tenant columns reached | Bound value | `Scope.Tenant` refused with `ErrTenantInvalid` |
|---|---|---|
| Strings only | The text as given | Never |
| A UUID, alone or beside strings | The UUID's canonical text (lowercase, hyphenated) at every column, so a string column beside a UUID one holds that canonical text | Text that is not a UUID |
| Integers of any widths and signedness | An `int64` (a `uint64` when every column is unsigned), the decimal digits with an optional minus sign | Text that is not decimal digits, or a value out of range of any reached column's Go kind (`3000000000` beside an `int32` column) |
| None, or only pair tables | The text as given | Never |
| A string or UUID beside an integer | — | `Relate` refuses the declaration |

`Relate` refuses a relation that would make any resource reach a string or UUID tenant column and an integer one, whichever declaration comes last: a relation on a target refuses itself when an owner already relating to that target would reach both. A refused relation is not declared, and the graph stays as it was. `Relate` therefore changes the tenant key of every resource that reaches its owner; declare the whole graph before serving. A `WithPaths` view is a value for one request that shares its resource's patch schemas and tenant graph, so `Relate` refuses one as the owner or the target; relate the resources it is derived from.

A pair table's `Tenant` column has no Go type. It holds the request's one tenant in the one type: the type of the typed tenant columns the resource reaches, or the text when it reaches none. An owner without a tenant column whose relations reach an integer tenant column binds an integer to every one of its pair tables, so declare those pair tenant columns with the integer type in the database; to keep a pair table's tenant as text, keep integer tenant columns out of that resource's reach.

### Governed lists

`Scope.Governance` replaces the tenant floor: the list reads the rows the governance tree admits, of any tenant — a grant of another tenant's row, a parent tenant's view of its children's. A row's relations are its own tenant's. Every tenant-scoped related row, at every depth — a target's `TenantColumn`, a pair table's `Tenant` — is compared with the tenant column of the listed row it hangs from (`filter.RelationScope.RowTenant`) instead of being bound to one tenant:

```
WHERE … EXISTS (SELECT 1 FROM notes WHERE notes.document_id = documents.id
  AND notes.tenant_id = documents.tenant_id AND notes.deleted_at IS NULL AND notes.body = $1)
```

A granted row of tenant B is filtered, counted, faceted and included by B's related rows, and a row of tenant A hanging from it (A's note on B's document, A's pair to it) is not its relation. `Scope.Tenant` is neither required nor used. Under the floor every listed row is the request's tenant, so the two agree: a governance tree that admits only the request's tenant lists exactly what the floor lists, and a nil or empty tree is the floor (`ErrTenantRequired` without `Scope.Tenant`). Includes join the owner's table to read each owner's tenant (`filter.WithIncludeOwner`), and facets project it beside the owner's key, both under the list's `RelationScope.RowTenant`. Updates refuse governance (`ErrUpdateScope`).

The comparison is between two columns, so their types must meet. A UUID tenant column and a string one compare by the UUID's canonical text, `CAST(… AS TEXT)` on the UUID side, which a string tenant column beside a UUID one holds ([Tenant key](#tenant-key)); integer columns of any width compare as they are. A pair table's `Tenant` column declares no type and is compared as it is with the owner's: give it the type of the owner's tenant column, or PostgreSQL refuses the statement (`operator does not exist: text = uuid`).

A resource without a tenant column has rows of no tenant: its governed lists hold relations to `Scope.Tenant`, as the floor does.

### Read columns

A relation pairs owner and target rows by one column on each side: the owner's key or `References` column and a has-many's foreign key on the target, a belongs-to's foreign key on the owner and its target's key or `References` column. Filtering by the relation discloses both columns' values by which rows match, and an include or facet returns the pairs outright, so `Relate` holds each side to its own resource's `ReadColumns`:

| Relation | Each correlation column must be |
|---|---|
| Filtered only | Read, or named in that resource's `QueryOnlyColumns` |
| `Includable()` or `Facetable()` | Read |

A many-to-many correlates on both resources' keys, which are always read; its pair table is no resource. A refused relation is not declared: `relation "members": members does not read column "team_id"` names the resource and the column to read, or to name in `QueryOnlyColumns`.

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

The output above is `ExampleResource_Relate`, checked by the unit lane. A many-to-many with a target filter joins the target inside the subquery: `EXISTS (SELECT 1 FROM device_tags rel1__ JOIN tags ON tags.id = rel1__.tag_id WHERE rel1__.device_id = devices.id AND rel1__.tenant_id = $1 AND tags.tenant_id = $2 AND tags.label = $3)`. A target's [computed column](queries.md#computed-columns) (`parse.ReadExpr`) filters as the target's expression for the owner's dialect, and fails the build where the subquery aliases the target's table, as a relation back to the owner's table does; any other target field computed from something other than its table's column cannot be used through a relation (the build fails); a relation without a target resource takes no target filter (E2083).

### Outside SQL

A backend that holds rows in memory evaluates relation predicates as memberships: `filter.ResolveMemberships(ctx, f, lookup, filter.ResolveRelationScope(filter.RelationScope{Tenant: tenant, Related: related}))` replaces each one, before any row is read, with a comparison on the owner's correlation field (`FieldSchema.RelatedBy`, the key or a belongs-to's foreign key): `$some` is the field among the related rows' correlation values, `$none` the field outside them or NULL, exactly as `NOT EXISTS` admits a NULL foreign key. The related rows are a `filter.Lookup` over the target (or pair) table — its live rows of the tenant, the projected column never NULL — and, for a target filter, `Lookup.Filter`, the target's filter with its policy composed in. A many-to-many is the pair rows whose target is in the target's lookup (`WhereMember`); depth-two relations resolve inside the target's filter first.

Lookups with a `Filter` go only to a `filter.RelationLookup`, whose `LookupFilteredValues` evaluates the filter with the store's own evaluator; a store that is only a `MembershipLookup` refuses those requests (E2083) rather than ignore the filter and over-match. Without a lookup, or without a tenant for a tenant-scoped relation, the request is refused too. `relation_declared_cases_test.go` answers every case both ways — `EXISTS` and resolved memberships — on SQLite and PostgreSQL and requires the same rows.

A lookup reads one tenant's related rows, and a [governed list](#governed-lists)'s rows span tenants. Select the rows with the composed policy (`filter.ApplyPolicyConstraints` over `filter.ComposeConstraints(scope, governance, policy)`), group them by tenant, and resolve the filter once per tenant with that tenant's `RelationScope.Tenant`: each row is evaluated with its own tenant's resolved filter. Includes resolve the same way, each tenant's owners with `ResolveInclude(…, filter.WithIncludeRelationScope(filter.RelationScope{Tenant: tenant}))`; `ResolveMemberships` and `ResolveInclude` refuse a `RowTenant`. `governed_list_cases_test.go` lists rows held in memory this way and requires the rows and includes SQL returns.

### Include related keys

Declare `Includable()` on a has-many or many-to-many and a list may ask for its related keys: `include=commands,tags` puts each page row's visible related keys in the page's `_relations` — a has-many's live children in the tenant, a many-to-many's pairs whose target is live and in the tenant (or, policy-scoped, admitted by `Scope.Related`). A list includes at most `filter.MaxIncludes` (3) relations (E2083 beyond). A belongs-to's key is the row's own foreign-key column, so it has nothing to include.

### Facet by related keys

Declare `Facetable()` on a has-many or many-to-many and `facets=tags` counts the matched rows per visible related key — distinct owners per key, the related rows scoped as includes and filters are. A belongs-to facets through its foreign-key column instead.

### Change membership

Declare `Writable()` on a many-to-many and a patch changes its membership with target keys: a merge patch `{"tags": ["red", "blue"]}` replaces the set, `{"tags": []}` or `{"tags": null}` clears it, and a JSON Patch `add` / `remove` on `/tags` with a list of at least one key adds or removes keys. A key is never null (E2118); the parser decides each operation's value, so a membership change it accepts always renders. Keys travel as the target's key does: a target key tagged `json:",string"` takes them as JSON strings (`{"tags":["5"]}`), as responses write it and the relation's filter shorthands take it, and the schema document marks the path `"wire": "string"` ([quoted values](schema-configuration.md#quoted-values)). `BuildUpdate` returns the owner statement and `RelationStatements` the membership statements for the keys it returned (`parsepgx.Update` runs both in one transaction). Pairs are made only with targets the request may relate — live, in the tenant, or admitted by the target's policy — by selecting the owner and target rows rather than binding raw keys, and a replace or remove reaches only pairs to such targets, so pairs the request cannot see stay:

```sql
INSERT INTO "device_tags" ("device_id", "tag_id", "tenant_id")
SELECT "owner__"."id", "target__"."id", "owner__"."tenant_id" FROM "devices" owner__, "tags" target__
WHERE "owner__"."id" = ANY($1) AND "owner__"."tenant_id" = $2 AND "target__"."id" = ANY($3)
  AND "target__"."tenant_id" = $4 AND "target__"."deleted_at" IS NULL ON CONFLICT DO NOTHING
```

The owner keys and the target keys each bind as one array parameter (`= ANY($n)` on PostgreSQL, `IN (SELECT value FROM json_each(?n))` on SQLite), so a statement's parameter count is the same for one key as for a thousand; one operation names at most `PatchSchema.MaxRelationValues` keys (1000 by default, E2119 beyond, repeats counted once). A has-many is never written through its parent (that would insert or delete child rows); a belongs-to is written through its foreign-key column like any other column. A writable pair table's tenant comes from the owner or the target.

See [pair tables](pair-tables.md), [query options](queries.md#configuration), [patch compilation](patches.md#sql-compilation), and [integration responsibilities](integrations.md).
