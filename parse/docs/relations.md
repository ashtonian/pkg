# Relations

[Package overview](../README.md)

A relation lets a request filter rows by the rows they are related to. Declare it on the owner resource with `Resource.Relate` — a has-many, a belongs-to or a many-to-many to another `Resource` — and requests filter by the related rows with the target's own filter. A `relation:"…"` struct tag declares a [pair table](pair-tables.md) instead: membership compared by the target's key alone, for a target that is not a `Resource` or a request that only matches related keys. Every relation predicate compiles to one correlated `EXISTS` / `NOT EXISTS`, so a row matches once however many related rows it has.

## Declare relations

Relate both resources after constructing them, so they can name each other, and before they serve requests:

```go
devices := parse.MustResource[Device](provider, "devices", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
commands := parse.MustResource[Command](provider, "commands", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
types := parse.MustResource[DeviceType](provider, "device_types", parse.TenantColumn("tenant_id"))
tags := parse.MustResource[Tag](provider, "tags", parse.TenantColumn("tenant_id"))

devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))     // commands.device_id = devices.id
devices.MustRelate("type", parse.BelongsTo(types, "type_id", parse.PolicyScoped()))      // device_types.id = devices.type_id
devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed()))
commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))
```

| Declaration | Relates | Correlation |
|---|---|---|
| `HasMany(target, fk)` | an owner to the target rows whose `fk` holds its key; `fk` is a column, or a `[]string` of one per key column | `target.fk = owner.<key>`, column by column; `References(cols...)` names other owner columns |
| `BelongsTo(target, fk)` | an owner to the target row its `fk` names; `fk` is a column, or a `[]string` of one per target key column | `target.<key> = owner.fk`, column by column; `References(cols...)` names other target columns |
| `ManyToMany(target, junction)` | owners and targets through a pair table, a `Junction` (`Through(table, source, target)` builds one without a tenant column, each side a column or a `[]string`) | `junction.Source = owner.<key>`, `target.<key> = junction.Target`, column by column |

`Relate` checks every column against both resources' types and returns an error; `MustRelate` panics. The columns a relation correlates on, the owner's (its key or `References` columns) and the target's (its key and, for a has-many or belongs-to, the foreign key or `References` columns), are keys of any scalar type, each compared as a filter compares its type ([keys](schema-configuration.md#keys)): a decimal by value, so a foreign key stored as `1.50` joins the key `1.5`, a timestamp at its instant. A document, array or geometry column has no equality to join by and is refused, naming the column. A has-many's or belongs-to's paired columns are of one type, a string and a UUID alike, so the join compares them as that type. A relation's name must be a name no server policy already resolves on the owner: a request field, a system field, or any [policy field](queries.md#policy-fields), the owner's columns among them. Otherwise the relation would change what a policy on that name constrains. A relation may lead back to its own table (an asset's parent); the query aliases it so it does not shadow the owner.

| Option | Effect |
|---|---|
| `Indexed()` | An index leads with the correlation columns (the foreign key, or the pair table's owner columns): the relation costs 8 instead of 80 against `MaxCost` (default 50). A belongs-to correlates on the target's key and is always indexed. |
| `PolicyScoped()` | The related rows are those the server's policy for the target table admits (`Scope.Related[<table>]`), not the request's tenant: rows another tenant shares, such as platform device types. A list using the relation without that policy fails with `filter.ErrRelatedPolicyRequired`. |
| `References(columns...)` | The columns a foreign key holds when they are not the referenced resource's key, paired with the foreign key's columns by position. An includable or facetable has-many correlates on the owner's key, so it takes no `References` |
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

`Scope.Governance` replaces the tenant floor: the list reads the rows the governance tree admits, of any tenant — a grant of another tenant's row, a parent tenant's view of its children's. A tree with no constraint anywhere in it (`{}`, `{Or: [{}]}`, empty groups at any depth) constrains nothing and is no governance: the list keeps the tenant floor, and without `Scope.Tenant` is `ErrTenantRequired`, so a user whose grants compile to nothing reads their own tenant's rows, never every tenant's. A row's relations are its own tenant's. Every tenant-scoped related row, at every depth — a target's `TenantColumn`, a pair table's `Tenant` — is compared with the tenant column of the listed row it hangs from (`filter.RelationScope.RowTenant`) instead of being bound to one tenant. Documents governed to tenant A's rows and B's `b1`, filtered by `{"notes":{"$some":{"body":"urgent"}}}`:

<!-- output of ExampleResource_BuildList_governed -->
```text
WHERE (EXISTS (SELECT 1 FROM "notes" WHERE "notes"."document_id" = "documents"."id" AND "notes"."tenant_id" = "documents"."tenant_id" AND "notes"."deleted_at" IS NULL AND "notes"."body" = $1) AND ("documents"."tenant_id" = $2 OR "documents"."id" = $3)) …
[urgent A b1]
```

A granted row of tenant B is filtered, counted, faceted and included by B's related rows, and a row of tenant A hanging from it (A's note on B's document, A's pair to it) is not its relation. `Scope.Tenant` is neither required nor used. Under the floor every listed row is the request's tenant, so the two agree: a governance tree that admits only the request's tenant lists exactly what the floor lists, and a nil or empty tree is the floor (`ErrTenantRequired` without `Scope.Tenant`). Includes join the owner's table to read each owner's tenant (`filter.WithIncludeOwner`), and facets project it beside the owner's key, both under the list's `RelationScope.RowTenant`. Updates refuse governance (`ErrUpdateScope`).

The comparison is between two columns, so their types must meet. A UUID tenant column and a string one compare by the UUID's canonical text, `CAST(… AS TEXT)` on the UUID side, which a string tenant column beside a UUID one holds ([Tenant key](#tenant-key)); integer columns of any width compare as they are. A pair table's `Tenant` column declares no type and is compared as it is with the owner's: give it the type of the owner's tenant column, or PostgreSQL refuses the statement (`operator does not exist: text = uuid`).

A resource without a tenant column has rows of no tenant: its governed lists hold relations to `Scope.Tenant`, as the floor does.

### Read columns

A relation pairs owner and target rows by one column on each side, or by a column on each side per key column for a [key of several columns](#keys-of-several-columns): the owner's key or `References` column and a has-many's foreign key on the target, a belongs-to's foreign key on the owner and its target's key or `References` column. Filtering by the relation discloses both columns' values by which rows match, and an include or facet returns the pairs outright, so `Relate` holds each side to its own resource's `ReadColumns`:

| Relation | Each correlation column must be |
|---|---|
| Filtered only | Read, or named in that resource's `QueryOnlyColumns` |
| `Includable()` or `Facetable()` | Read |

A many-to-many correlates on both resources' keys, which are always read; its pair table is no resource. A refused relation is not declared, and the error names the resource and the column to read, or to name in `QueryOnlyColumns`. An includable has-many whose foreign key the target only queries:

<!-- output of ExampleResource_Relate_readColumns -->
```text
parse: teams: relation "members": members does not read column "team_id"
```

### Keys of several columns

A resource keyed by several columns (`parse.PrimaryKey("warehouse_id", "sku")`, [keys](schema-configuration.md#keys)) relates as any other, the foreign key or each side of the pair table naming a column per key column, in key order:

```go
stock := parse.MustResource[Stock](provider, "stock", parse.PrimaryKey("warehouse_id", "sku"))
key := []string{"warehouse_id", "sku"}
stock.MustRelate("moves", parse.HasMany(moves, key, parse.Indexed(), parse.Includable()))
stock.MustRelate("tags", parse.ManyToMany(tags, parse.Through("stock_tags", key, "tag_id"), parse.Writable()))
moves.MustRelate("stock", parse.BelongsTo(stock, key))
tags.MustRelate("stock", parse.ManyToMany(stock, parse.Through("stock_tags", "tag_id", key), parse.Writable()))
bins.MustRelate("stock", parse.HasMany(stock, "warehouse_id"))
```

`HasMany`, `BelongsTo` and `Through` take a column (`"warehouse_id"`) or a `[]string`; `References` and `Junction`'s `Source` and `Target` take lists. Each list pairs by position with the key it holds, and the correlation is the AND of the pairs' equalities: `"moves"."warehouse_id" = "stock"."warehouse_id" AND "moves"."sku" = "stock"."sku"`. A relation whose lists are one column each compiles exactly as before. `Relate` refuses a list of another length than its key, a column named twice, a column its table lacks, and a pair of columns of different key kinds (a string or UUID beside an integer), naming the relation and the columns: `HasMany(moves, "sku")` on stock is `relation "moves": foreign key (sku) pairs by position with stock's key (warehouse_id, sku), so it names as many columns`. A has-many need not hold the whole of the target's key: `bins` relates the stock rows whose `warehouse_id` is its key.

| Use | A relation whose owner key has several columns | A relation whose target key has several columns |
|---|---|---|
| `$some`, `$none`, `$is`, `$is_not`, `$null` | As any relation | As any relation |
| Key shorthands (`{"stock":"…"}`, `$eq`, `$ne`, `$in`, `$nin`) | As any relation | Refused (E2083): one value cannot name the key; filter the target with `$some` |
| Include, facet | Owners named by key text | Targets named by key text |
| Membership patch | Owner keys are the update's target (`ByKey`, `ByKeys`) | Each key is an array of a value per column: `{"stock":[[1,"a"],[2,"b"]]}` |
| [Outside SQL](#outside-sql) | The owner's key fields among the related rows' keys (`OpKeyIn`) | Target keys compared column by column, each as its type compares |

A key of several columns is named in an include and a facet by its text, `core.KeyText`: the compact JSON array of its columns' texts, `["1","a"]`, which PostgreSQL's `array_to_json` and SQLite's `json_array` write for the same row and Go writes for a `parse.Key`. So `_relations.moves` maps `["1","a"]` to `["10","11"]`, and a facet on `tags.stock` counts the bucket `["1","a"]`; `core.KeyText(key)` of `Resource.Key(item)` finds a page row's entry. A one-column key is its value's text, as before. The schema document lists a membership path's target key columns (`key`: each column's `data_type`, `wire` and `number_kind`), from which the TypeScript SDK validates each key.

## Filter by related rows

| Request | Means |
|---|---|
| `{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}` | some visible command is pending with more than two attempts — one command satisfies every predicate inside one `$some` |
| `{"commands":{"$none":{"status":"failed"}}}` | no visible command failed; devices without commands match |
| `{"type":{"$is":{"name":{"$like":"pump%"}}}}` | the device's type is visible and matches; `$is_not` is its negation, so devices without a visible type match it |
| `{"commands":"c1"}`, `{"commands":{"$in":["c1","c2"]}}` | some visible command has that key; `$ne` / `$nin` are `$none` |
| `{"commands":{"$null":true}}` | no visible command (`$none: {}`); `$null: false` is `$some: {}` |

`$some` / `$none` apply to has-many and many-to-many relations, `$is` / `$is_not` to belongs-to. A NULL foreign key is tested with the foreign-key column's own `$null` (`{"type_id":{"$null":true}}`), which says nothing about whether the type is visible. The target's own relations may be used inside the target's filter, two levels deep (`devices → commands → receipts`, E2074 beyond), a key comparison on a relation (`{"receipts":"r1"}`, `$in`, `$ne`, `$nin`, `$null`) counting a level as the `$some` / `$none` it abbreviates does; a filter holds at most 8 relation predicates (E2089 beyond). A relation predicate costs the relation (8 indexed, 80 not) plus its nested filter's cost. Flat parameters (`?commands=c1`) and policy constraints that name a relation take the same path.

<!-- output of ExampleResource_Relate -->
```text
{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}
WHERE (EXISTS (SELECT 1 FROM "commands" WHERE "commands"."device_id" = "devices"."id" AND "commands"."tenant_id" = $1 AND ("commands"."attempts" > $2 AND "commands"."status" = $3)) AND "devices"."tenant_id" = $4) … [t1 2 pending t1]
{"commands":{"$none":{"status":"failed"}}}
WHERE (NOT EXISTS (SELECT 1 FROM "commands" WHERE "commands"."device_id" = "devices"."id" AND "commands"."tenant_id" = $1 AND "commands"."status" = $2) AND "devices"."tenant_id" = $3) … [t1 failed t1]
{"tags":{"$some":{"label":"red"}}}
WHERE (EXISTS (SELECT 1 FROM "device_tags" "rel1__" JOIN "tags" ON "tags"."id" = "rel1__"."tag_id" WHERE "rel1__"."device_id" = "devices"."id" AND "rel1__"."tenant_id" = $1 AND "tags"."tenant_id" = $2 AND "tags"."deleted_at" IS NULL AND "tags"."label" = $3) AND "devices"."tenant_id" = $4) … [t1 t1 red t1]
```

A many-to-many with a target filter, the last, joins the target to the pair table inside the subquery. A target's [computed column](queries.md#computed-columns) (`parse.ReadExpr`) filters as the target's expression for the owner's dialect, and fails the build where the subquery aliases the target's table, as a relation back to the owner's table does; any other target field computed from something other than its table's column cannot be used through a relation (the build fails); a relation without a target resource takes no target filter (E2083).

### Outside SQL

A backend that holds rows in memory evaluates relation predicates as memberships: `filter.ResolveMemberships(ctx, f, lookup, filter.ResolveRelationScope(filter.RelationScope{Tenant: tenant, Related: related}))` replaces each one, before any row is read, with a comparison on the owner's correlation field (`FieldSchema.RelatedBy`, the key or a belongs-to's foreign key): `$some` is the field among the related rows' correlation values, `$none` the field outside them or NULL, exactly as `NOT EXISTS` admits a NULL foreign key. The related rows are a `filter.Lookup` over the target (or pair) table — its live rows of the tenant, no projected column NULL — and, for a target filter, `Lookup.Filter`, the target's filter with its policy composed in. A many-to-many is the pair rows whose target is in the target's lookup (`WhereMember`); depth-two relations resolve inside the target's filter first. A relation that correlates [keys of several columns](#keys-of-several-columns) projects them all (`Lookup.ProjectKey`, each answer a `core.Key` of a value per column), a many-to-many's pair rows test the target's key with `WhereKeyMember`, and the owner's fields compare with the keys as a key set (`OpKeyIn`), as the correlated `EXISTS` equates every column. Every condition the resolver writes on a key or foreign-key column carries its columns' types (`LookupCond.Types`), so the store compares a pair's `1.50` with a target's `1.5`, and a timestamp at its instant, as SQL does; a store answers through `filter.MatchLookupRow`, which honours them.

Lookups with a `Filter` go only to a `filter.RelationLookup`, whose `LookupFilteredValues` evaluates the filter with the store's own evaluator; a store that is only a `MembershipLookup` refuses those requests (`filter.ErrRelationUnresolvable`) rather than ignore the filter and over-match. Without a lookup the request is refused the same way, and without a tenant for a tenant-scoped relation with `filter.ErrRelationTenantRequired`: both are the server's fault, which the handler answers with its own 500. The corpus's declared-relation cases ([`internal/corpus/relation.go`](../internal/corpus/relation.go)) answer every case both ways — `EXISTS` and resolved memberships — on SQLite and PostgreSQL and require the same rows. Its resolved-store cases do the same for keys of several columns and of decimal and timestamp columns, filters and includes, through a store holding the tables in memory and one running each lookup's SQL.

A lookup reads one tenant's related rows, and a [governed list](#governed-lists)'s rows span tenants. Select the rows with the composed policy (`filter.ApplyPolicyConstraints` over `filter.ComposeConstraints(scope, governance, policy)`), group them by tenant, and resolve the filter once per tenant with that tenant's `RelationScope.Tenant`: each row is evaluated with its own tenant's resolved filter. Includes resolve the same way, each tenant's owners with `ResolveInclude(…, filter.WithIncludeRelationScope(filter.RelationScope{Tenant: tenant}))`; `ResolveMemberships` and `ResolveInclude` refuse a `RowTenant`. The corpus's governed-list cases ([`internal/corpus/list.go`](../internal/corpus/list.go)) list rows held in memory this way and require the rows and includes SQL returns.

### Include related keys

Declare `Includable()` on a has-many or many-to-many and a list may ask for its related keys: `include=commands,tags` puts each page row's visible related keys in the page's `_relations` — a has-many's live children in the tenant, a many-to-many's pairs whose target is live and in the tenant (or, policy-scoped, admitted by `Scope.Related`). A list includes at most `filter.MaxIncludes` (3) relations (E2083 beyond). A belongs-to's key is the row's own foreign-key column, so it has nothing to include. `_relations` groups related keys by the owner's key, so an includable relation correlates on that key, not on `References` columns; owner and related keys of several columns are named by their text ([keys of several columns](#keys-of-several-columns)).

### Facet by related keys

Declare `Facetable()` on a has-many or many-to-many and `facets=tags` counts the matched rows per visible related key — distinct owners per key, the related rows scoped as includes and filters are. A facetable relation correlates on the owner's key, which it counts, and a related key of several columns is its bucket's text (`["1","a"]`). Text keys are grouped by code point, as includes name them, whatever the junction column's collation: a case-insensitive column's `Red` and `red` are two buckets. A belongs-to facets through its foreign-key column instead.

### Change membership

Declare `Writable()` on a many-to-many and a patch changes its membership with target keys: a merge patch `{"tags": ["red", "blue"]}` replaces the set, `{"tags": []}` or `{"tags": null}` clears it, and a JSON Patch `add` / `remove` on `/tags` with a list of at least one key adds or removes keys. A key is never null (E2118); the parser decides each operation's value, so a membership change it accepts always renders. Keys travel as the target's key does: a target key tagged `json:",string"` takes them as JSON strings (`{"tags":["5"]}`), as responses write it and the relation's filter shorthands take it, and the schema document marks the path `"wire": "string"` ([quoted values](schema-configuration.md#quoted-values)). A target key of several columns is an array of a value per column, in key order (`{"stock":[[1,"a"],[2,"b"]]}`), each value taking its column's type and wire form; the schema document lists the columns in the path's `key`. `Update` runs the owner statement, then the membership statements for the keys it returned, in one transaction. Pairs are made only with targets the request may relate — live, in the tenant, or admitted by the target's policy — by selecting the owner and target rows rather than binding raw keys, and a replace or remove reaches only pairs to such targets, so pairs the request cannot see stay. A JSON Patch `add` of `["red","blue"]` to `/tags` on device `d1`:

`ExampleResource_Update_membership` adds the tags red, blue (deleted) and green (another tenant's) to a device of tenant t1; the statement pairs the device with red alone:

<!-- output of ExampleResource_Update_membership -->
```text
pairs written: 1
d1 red t1
```

The owner keys and the target keys each bind as one array parameter (`= ANY($n)` on PostgreSQL, `IN (SELECT value FROM json_each(?n))` on SQLite), and keys of several columns as one row-value set (`(a, b) IN (SELECT …)`, [keys](schema-configuration.md#keys)), so a statement's parameter count is the same for one key as for a thousand; one operation names at most `PatchSchema.MaxRelationValues` keys (1000 by default, E2119 beyond, repeats counted once). A has-many is never written through its parent (that would insert or delete child rows); a belongs-to is written through its foreign-key column like any other column. A writable pair table's tenant comes from the owner or the target.

See [pair tables](pair-tables.md), [query options](queries.md#configuration), [patch compilation](patches.md#sql-compilation), and [integration responsibilities](integrations.md).
