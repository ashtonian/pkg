# Generated SDKs

[SDK README](../README.md) · [Go client for generated SDKs](../../../client/README.md#for-generated-sdks) · [Integrations](../../../docs/integrations.md)

A generator that emits a TypeScript SDK for a server built on parse, from an OpenAPI document, the Go types or a schema of its own, keeps what is specific to that API: its types and a thin binding of each operation. It takes everything else from `@ashtonian/parse`: the transport, the operations parse defines, a request method for the routes parse does not, the errors, cursor iteration and validation. The generated code then holds no second HTTP client, no second encoding of parse's query grammar and no second error model, so nothing in it drifts from the server's contract, which this package is tested against.

## What the generated SDK keeps

- *Types*: a row type per resource (`T`), its key type (`K`: one value, or a tuple of a value per key column in key order), and the body and response types of the routes parse does not define: a create input, a replacement, an action's answer.
- *A binding per operation*: one line per route, naming it and passing its types. A list, facet or patch operation is the handle's own method; any other route is a `request` call.
- *Its configuration surface*: whatever the SDK exposes for the base URL, headers, timeout and authentication maps onto `ClientConfig`.

It keeps no fetch wrapper, URL joining, query-string encoding, key escaping, merge-patch typing, error classes, pagination loop or validator: the package owns each of them.

## What it takes from @ashtonian/parse

| Concern | From `@ashtonian/parse` | Generated code it replaces |
|---|---|---|
| Transport: base URL, default headers, timeout, abort | `createClient(config)` ([configuration](../README.md#configuration)) | A fetch wrapper, URL joining, timeout and signal plumbing |
| Authentication, retry | A wrapper passed as `fetch` ([below](#authentication-and-retry)) | Interceptors and per-call token handling |
| List, facets, patch one, patch many, schema | A handle's `list`, `facets`, `patch`, `patchMany` and `schema` ([server contract](../README.md#server-contract)) | List and patch functions; the encoding of filters, sorts, `id[]` and key segments |
| Create, get, replace, delete, actions on a row | `request` on a handle or on the client ([other routes](../README.md#other-routes)) | A fetch call per operation |
| Filters and sorts | `filter`, `and`, `or`, `not`, `sort` and the typed `ListOptions` ([filtering](../README.md#filtering)) | Filter builders |
| Errors | `ApiError`, which reads parse's error body and RFC 9457 problems alike; `ResponseError`, `NonFiniteNumberError` and the schema errors ([errors](../README.md#errors)) | Error classes and response decoding |
| Iteration | `listAll`, a lazy async iterator over the pages by cursor | Cursor loops |
| Validation | `validateBeforeSend`, `validator()` and `SchemaValidator`, which read the server's schema document ([schema discovery and validation](../README.md#schema-discovery--validation)) | Generated validators of filters, sorts and patches |
| Merge-patch input | `PatchInput<T>`, `MergeDocument<T>` ([patching](../README.md#patching)) | Partial and nullable patch types |

## A binding

```typescript
// devices.ts, generated from the API's description.
import { type ClientConfig, createClient, type RequestOptions } from "@ashtonian/parse";

export interface Device {
  id: string;
  name: string;
  labels: Record<string, string>;
}

export interface NewDevice {
  name: string;
  labels?: Record<string, string>;
}

export function devicesAPI(config: ClientConfig) {
  const devices = createClient(config).resource<Device>("devices");
  return {
    // parse's operations are the handle's own methods, which read no receiver.
    list: devices.list,
    listAll: devices.listAll,
    facets: devices.facets,
    patch: devices.patch,
    // The server's other routes go through request.
    create: (input: NewDevice, reqOpts?: RequestOptions) =>
      devices.request<Device, NewDevice>("POST", { body: input }, reqOpts),
    get: (id: string, reqOpts?: RequestOptions) =>
      devices.request<Device>("GET", { key: id }, reqOpts),
    remove: (id: string, reqOpts?: RequestOptions) =>
      devices.request<undefined>("DELETE", { key: id, response: "none" }, reqOpts),
    reboot: (id: string, reqOpts?: RequestOptions) =>
      devices.request<undefined>("POST", { key: id, path: "reboot", response: "none" }, reqOpts),
  };
}
```

`create` sends `POST /devices` with the input as its JSON body, `get` sends `GET /devices/{id}` and `reboot` sends `POST /devices/{id}/reboot`, each key value escaped into its own path segment as `patch` writes it. `remove` and `reboot` answer 204, so they take `response: "none"` and resolve `undefined`. Without it a 204 is a `ResponseError`, as it is for `list` and `patch`. A route that is not below a resource is `client.request(method, path, opts)`.

`R` and `B`, the response and body types the binding passes, are never checked at run time: the binding states them, so a generator emits them from the same description as the server's handlers. A request never reads the schema, so `validateBeforeSend` changes nothing it sends.

## Authentication and retry

Static credentials go in `ClientConfig.headers`. Anything that changes per request is a `fetch` wrapper, which every request goes through, the schema fetches and `request` included:

```typescript
import { createClient } from "@ashtonian/parse";

const authenticated: typeof fetch = async (input, init) => {
  const headers = new Headers(init?.headers);
  headers.set("Authorization", `Bearer ${await tokens.current()}`);
  return fetch(input, { ...init, headers });
};

const client = createClient({ baseURL: "https://api.example.com/api", fetch: authenticated });
```

A retry wrapper is a `fetch` too. It decides which requests are safe to repeat: a GET is, a list over POST is, and a create is not unless its route takes an idempotency key, sent in the call's `headers`. It passes `init.signal` on to each attempt, so a retry stops when the call's signal or the client's `timeout` fires; that deadline covers the call, every attempt together.

## Migrating a generated SDK

1. Replace the generated HTTP layer with one `createClient`, mapping the SDK's configuration onto `ClientConfig`. Authentication and retry become a `fetch` wrapper.
2. Replace each list, facet and patch operation with the handle's method and delete the generated query encoding. `list` sends parse's parameters, and past `maxURLLength` the same request as a POST body; `patch` sends the merge patch to the row's path.
3. Bind every other operation through `request`, with the response type as `R` and the input type as `B`. A route answered 204 takes `response: "none"` and `R` `undefined`.
4. Type a resource whose key has several columns with its key tuple, `resource<Stock, [string, string]>("stock")`, so `patch` and `request` take that tuple and a key of another arity does not compile.
5. Catch `ApiError` where the SDK had its own error types: its `status`, `code` (parse's E-code, or the problem's), `message`, `problem` and `errors` (the problem's field errors).
6. Delete generated validators of filters, sorts and patches; turn on `validateBeforeSend` where a request should be checked against the server's schema before it is sent.
7. Replace cursor loops with `listAll`.

## A generated Go SDK

A generated Go SDK builds on [`parse/client`](../../../client/README.md), this package's Go counterpart. What such an SDK keeps and what it takes from there is in that README's [For generated SDKs](../../../client/README.md#for-generated-sdks).
