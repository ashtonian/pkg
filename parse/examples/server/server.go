package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// Product is the resource: its tags declare what clients may filter, sort,
// facet, search and patch.
type Product struct {
	ID        string         `json:"id"         db:"id"         parse:"filter,sort"`
	Name      string         `json:"name"       db:"name"       parse:"filter,sort,patch" filter_ops:"eq,ilike,contains" label:"Name"`
	Category  string         `json:"category"   db:"category"   parse:"filter,facet,patch" facet:"terms:10" enum:"electronics,clothing,books,home" label:"Category"`
	Price     float64        `json:"price"      db:"price"      parse:"filter,sort,facet,patch" filter_ops:"gte,lte" facet:"range:0-50,50-100,100+" label:"Price"`
	Labels    map[string]any `json:"labels"     db:"labels"     parse:"filter,patch" indexed:"true"`
	CreatedAt time.Time      `json:"created_at" db:"created_at" parse:"filter,sort" sort_default:"desc"`
}

const (
	// productsPath is the products collection; one product is below it.
	productsPath    = "/api/products"
	defaultPageSize = 25
	maxPageSize     = 100
	// maxPatchIDs bounds a batch patch as maxPageSize bounds a list page:
	// the answer carries every updated row.
	maxPatchIDs = maxPageSize
)

// The example's own error codes. Parse's catalog (parse.Codes) holds request
// errors; whether a row exists, or the server failed, is the application's
// to say.
const (
	codeBadRequest = "E4001"
	codeNotFound   = "E4004"
	codeInternal   = "E5001"
	codeNotReady   = "E5003" // the database does not answer
	codeTimeout    = "E5004" // the request timeout expired
)

// database is what the server needs of the database it runs on, which
// parseprovider.Database holds: the provider its resources are built on,
// and a ping for the readiness route.
type database interface {
	Provider() *parse.Provider
	Ping(ctx context.Context) error
}

// server serves the routes the TypeScript SDK and parse/client call: lists
// over GET and, for a filter too large for a URL, as a POST body to the same
// path; OPTIONS facets; PATCH merge patches of one product or of the
// products a batch's id[] keys name; GET schema discovery of one resource or
// of all of them. It also answers a readiness probe.
type server struct {
	db             database
	products       *parse.Resource[Product]
	log            *slog.Logger
	requestTimeout time.Duration
	// report is the parsehttp hook every response is written with.
	report parsehttp.Option
}

// Schema discovery's routes and the readiness probe, logged at trace:
// clients and orchestrators poll them.
const (
	schemaRoute  = "GET /api/{resource}/schema"
	schemasRoute = "GET /api/schemas"
	readyRoute   = "GET /readyz"
)

// newProducts is the products resource on p, built once at startup.
func newProducts(p *parse.Provider) (*parse.Resource[Product], error) {
	products, err := parse.NewResource[Product](p, "products", parse.PageSize(defaultPageSize, maxPageSize))
	if err != nil {
		return nil, fmt.Errorf("products resource: %w", err)
	}
	return products, nil
}

// newServer serves the routes on products, a resource of db's provider,
// each request under requestTimeout and logged once; newRequestID mints
// the ID of a request that arrives without one.
func newServer(db database, products *parse.Resource[Product], log *slog.Logger, requestTimeout time.Duration, newRequestID func() string) (http.Handler, error) {
	if requestTimeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}
	reg := parse.NewSchemaRegistry()
	err := parse.RegisterResource(reg, products)
	if err != nil {
		return nil, fmt.Errorf("schema registry: %w", err)
	}
	// A path segment per key column, so a resource keyed by several columns
	// routes as products, keyed by id, do.
	item, err := parsehttp.ItemPattern(products)
	if err != nil {
		return nil, fmt.Errorf("products item route: %w", err)
	}
	s := &server{db: db, products: products, log: log, requestTimeout: requestTimeout}
	s.report = parsehttp.WithErrorHandler(s.responseFailed)
	mux := http.NewServeMux()
	mux.HandleFunc(schemaRoute, parsehttp.SchemaHandler(reg, s.report))
	mux.HandleFunc(schemasRoute, parsehttp.SchemaListHandler(reg, s.report))
	mux.HandleFunc(readyRoute, s.ready)
	mux.HandleFunc("GET "+productsPath, s.list)
	mux.HandleFunc("POST "+productsPath, s.list)
	mux.HandleFunc("OPTIONS "+productsPath, s.facets)
	mux.HandleFunc("PATCH "+productsPath, s.patchMany)
	mux.HandleFunc("PATCH "+productsPath+"/"+item, s.patch)
	logged := requestLog{log: log, newID: newRequestID, routes: mux, traceRoutes: map[string]bool{schemaRoute: true, schemasRoute: true, readyRoute: true}}
	return logged.wrap(s.withDeadline(mux)), nil
}

// withDeadline serves each request under the request timeout. The deadline
// is on the request's context, so every query runs under it and fail can
// tell a request that ran out of time, or whose client left, from a server
// fault, whatever words the driver reports the interruption in.
func (s *server) withDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	req, err := parsehttp.ParseList(r, s.products)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.products.List(r.Context(), req, parse.Scope{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The list reads whole rows; the answer carries the members the
	// request's field mask names.
	err = parsehttp.WriteList(w, r, req, page, s.report)
	if err != nil {
		s.encodingFailed(w, r, err)
	}
}

func (s *server) facets(w http.ResponseWriter, r *http.Request) {
	req, err := parsehttp.ParseFacets(r, s.products)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts, err := s.products.Facets(r.Context(), req, parse.Scope{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A request that names no facet, or whose facets match no row, has no
	// buckets: the answer is an empty object, never null.
	if counts == nil {
		counts = map[string][]filter.FacetBucket{}
	}
	s.writeJSON(w, r, http.StatusOK, parsehttp.FacetsAnswer{Facets: counts})
}

// ready answers 200 while the database answers a ping within the request
// timeout, and 503 otherwise, so an orchestrator routes no traffic to a
// process that cannot reach its database.
func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	err := s.db.Ping(r.Context())
	if err != nil {
		s.log.WarnContext(r.Context(), "database not ready", slog.Any("error", err))
		s.writeJSON(w, r, http.StatusServiceUnavailable, parsehttp.Body{Code: codeNotReady, Message: "the database does not answer"})
		return
	}
	s.writeJSON(w, r, http.StatusOK, readyAnswer{Status: "ready"})
}

// readyAnswer is the readiness probe's answer.
type readyAnswer struct {
	Status string `json:"status"`
}

// patch applies a merge patch to the product its item route's key names, a
// path segment per key column.
func (s *server) patch(w http.ResponseWriter, r *http.Request) {
	_, ok := s.patchQuery(w, r)
	if !ok {
		return
	}
	// The key as the path spells it, each segment still escaped, so a key
	// of several columns reads back unambiguously.
	item := strings.TrimPrefix(r.URL.EscapedPath(), productsPath+"/")
	notFound := parsehttp.Body{Code: codeNotFound, Message: "product " + item + " not found"}
	key, err := parsehttp.ItemKey(r, s.products)
	if errors.Is(err, parse.ErrNoSuchKey) {
		// No product has a key its columns cannot hold.
		s.writeJSON(w, r, http.StatusNotFound, notFound)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.update(r, parse.ByKey(key))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(items) == 0 {
		s.writeJSON(w, r, http.StatusNotFound, notFound)
		return
	}
	s.writeJSON(w, r, http.StatusOK, parsehttp.PatchAnswer[Product]{Updated: items})
}

// patchMany applies one merge patch to the products its id[] keys name. A
// key with no product is a failed item, not a failed request, and so is a
// text the key column cannot hold, which names no product.
func (s *server) patchMany(w http.ResponseWriter, r *http.Request) {
	q, ok := s.patchQuery(w, r, "id[]")
	if !ok {
		return
	}
	ids := q["id[]"]
	if len(ids) == 0 || len(ids) > maxPatchIDs {
		s.writeJSON(w, r, http.StatusBadRequest, parsehttp.Body{
			Code:    codeBadRequest,
			Message: fmt.Sprintf("a batch patch names 1 to %d id[] keys, got %d", maxPatchIDs, len(ids)),
		})
		return
	}
	// Each id is read as its column's text, as the item route reads its
	// segment: request text reaches the plan only through ParseKey.
	keys := make([]parse.Key, 0, len(ids))
	for _, id := range ids {
		key, err := s.products.ParseKey(id)
		if errors.Is(err, parse.ErrNoSuchKey) {
			continue // no product has it: a failed item below
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		keys = append(keys, key)
	}
	var items []Product
	if len(keys) > 0 {
		var err error
		items, err = s.update(r, parse.ByKeys(keys...))
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	res := parsehttp.PatchAnswer[Product]{Updated: items}
	updated := make(map[string]bool, len(items))
	for _, p := range items {
		updated[p.ID] = true
	}
	for _, id := range ids {
		if !updated[id] {
			updated[id] = true // a key named twice fails once
			res.Failed = append(res.Failed, parsehttp.PatchFailure{ID: id, Code: codeNotFound, Error: "product " + id + " not found"})
		}
	}
	s.writeJSON(w, r, http.StatusOK, res)
}

// patchQuery decodes a patch route's query string, which names only the
// accepted parameters, and reports whether the request may proceed; it has
// answered the request when it may not. A query string that does not decode
// is refused whole, and so is a parameter the route does not read: a patch
// that ignored one, a filter meant to narrow it, would write rows the client
// did not mean to.
func (s *server) patchQuery(w http.ResponseWriter, r *http.Request, accepted ...string) (url.Values, bool) {
	q, err := parsehttp.Query(r)
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	names := slices.Sorted(maps.Keys(q))
	i := slices.IndexFunc(names, func(name string) bool { return !slices.Contains(accepted, name) })
	if i < 0 {
		return q, true
	}
	s.writeJSON(w, r, http.StatusBadRequest, parsehttp.Body{
		Code:    codeBadRequest,
		Message: fmt.Sprintf("%s %s reads no %q parameter", r.Method, r.URL.Path, names[i]),
	})
	return nil, false
}

// update applies the request's merge patch to target's products and returns
// the rows the target matched, as the update left them: those it wrote, and
// those a patch of what they already hold or an empty patch left.
func (s *server) update(r *http.Request, target parse.Target) ([]Product, error) {
	parsed, err := parsehttp.ParsePatch(r, s.products)
	if err != nil {
		return nil, err
	}
	res, err := s.products.Update(r.Context(), parsed, target, parse.Scope{})
	if err != nil {
		return nil, err
	}
	return slices.Concat(res.Changed, res.Unchanged), nil
}

// fail answers a parse error with its E-code and the status the code
// carries. Otherwise the request's context says why the work stopped: a
// request that ran out of time is 504, and one whose client left gets no
// answer, since no one reads it; neither is a server fault. A body the
// client did not finish sending is a bad request. Anything else is logged
// and answered as a 500 without internals.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, r, err, s.report) {
		return
	}
	ctx := r.Context()
	attrs := []any{slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err)}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		s.log.DebugContext(ctx, "request cancelled", attrs...)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		s.log.WarnContext(ctx, "request deadline exceeded", attrs...)
		s.writeJSON(w, r, http.StatusGatewayTimeout, parsehttp.Body{Code: codeTimeout, Message: "the request did not finish within its deadline"})
	case isBodyReadError(err):
		s.log.WarnContext(ctx, "reading request body", attrs...)
		s.writeJSON(w, r, http.StatusBadRequest, parsehttp.Body{Code: codeBadRequest, Message: "the request body could not be read"})
	default:
		s.log.ErrorContext(ctx, "request failed", attrs...)
		s.writeJSON(w, r, http.StatusInternalServerError, parsehttp.Body{Code: codeInternal, Message: "internal error"})
	}
}

func isBodyReadError(err error) bool {
	_, ok := errors.AsType[*parse.BodyReadError](err)
	return ok
}

// responseFailed logs what a response could not carry: a schema document
// or error details that would not encode are defects; a response that could
// not be written was dropped.
func (s *server) responseFailed(r *http.Request, err error) {
	level := slog.LevelWarn
	if errors.Is(err, parse.ECodeSchemaEncodingFailed) || errors.Is(err, parsehttp.ErrUnencodableDetails) {
		level = slog.LevelError
	}
	s.log.Log(r.Context(), level, "writing response", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
}

// writeJSON answers v, or 500 when v cannot be encoded.
func (s *server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	err := parsehttp.WriteJSON(w, r, status, v, s.report)
	if err != nil {
		s.encodingFailed(w, r, err)
	}
}

// encodingFailed answers 500 for a response that could not be encoded:
// parsehttp writes nothing until the whole body is encoded.
func (s *server) encodingFailed(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "encoding response", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	// A Body without details always encodes.
	_ = parsehttp.WriteJSON(w, r, http.StatusInternalServerError, parsehttp.Body{Code: codeInternal, Message: "internal error"}, s.report)
}
