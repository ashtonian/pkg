package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/patch"
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
)

// server serves the routes the TypeScript SDK and parse/client call: lists
// over GET and, for a filter too large for a URL, as a POST body to the same
// path; OPTIONS facets; PATCH merge patches of one product or of the
// products a batch's id[] keys name (through BuildUpdate); GET schema
// discovery.
type server struct {
	db             *sql.DB
	products       *parse.Resource[Product]
	log            *slog.Logger
	requestTimeout time.Duration
	// report is the parsehttp hook every response is written with.
	report parsehttp.Option
}

func newServer(db *sql.DB, log *slog.Logger, requestTimeout time.Duration) (http.Handler, error) {
	if requestTimeout <= 0 {
		return nil, fmt.Errorf("request timeout must be positive")
	}
	products, err := parse.NewResource[Product]("products", parse.PageSize(defaultPageSize, maxPageSize))
	if err != nil {
		return nil, fmt.Errorf("products resource: %w", err)
	}
	reg := parse.NewSchemaRegistry()
	if err := parse.RegisterResource(reg, products); err != nil {
		return nil, fmt.Errorf("schema registry: %w", err)
	}
	s := &server{db: db, products: products, log: log, requestTimeout: requestTimeout}
	s.report = parsehttp.WithErrorHandler(s.responseFailed)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/{resource}/schema", parsehttp.SchemaHandler(reg, s.report))
	mux.HandleFunc("GET /api/products", s.list)
	mux.HandleFunc("POST /api/products", s.list)
	mux.HandleFunc("OPTIONS /api/products", s.facets)
	mux.HandleFunc("PATCH /api/products", s.patchMany)
	mux.HandleFunc("PATCH /api/products/{id}", s.patch)
	return mux, nil
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	req, err := parsehttp.ParseList(r, s.products)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	plan, err := s.products.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := parsesql.QueryPage(ctx, s.db, s.products, plan)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, page)
}

func (s *server) facets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	req, err := s.products.ParseList(r.URL.Query())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	plan, err := s.products.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts := map[string][]filter.FacetBucket{}
	if plan.Facets != nil {
		counts, err = s.facetCounts(ctx, plan.Facets)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"facets": counts})
}

func (s *server) facetCounts(ctx context.Context, query *filter.FacetQuery) (map[string][]filter.FacetBucket, error) {
	rows, err := s.db.QueryContext(ctx, query.SQL, query.Args...)
	if err != nil {
		return nil, fmt.Errorf("facet products: %w", err)
	}
	results, err := filter.ScanFacetUnion(rows)
	err = errors.Join(err, rows.Close())
	if err != nil {
		return nil, fmt.Errorf("facet products: %w", err)
	}
	counts := filter.FacetMap(results)
	if counts == nil {
		counts = map[string][]filter.FacetBucket{}
	}
	return counts, nil
}

// patchResult answers a patch, as the SDK's PatchResult reads it.
type patchResult struct {
	Updated []Product   `json:"updated"`
	Failed  []itemError `json:"failed,omitempty"`
}

// itemError is one key of a batch patch that updated no row.
type itemError struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

func (s *server) patch(w http.ResponseWriter, r *http.Request) {
	if s.refuseParams(w, r) {
		return
	}
	id := r.PathValue("id")
	items, err := s.update(w, r, parse.ByID(id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(items) == 0 {
		s.writeJSON(w, r, http.StatusNotFound, parsehttp.Body{Code: codeNotFound, Message: "product " + id + " not found"})
		return
	}
	s.writeJSON(w, r, http.StatusOK, patchResult{Updated: items})
}

// patchMany applies one merge patch to the products its id[] keys name. A
// key with no product is a failed item, not a failed request.
func (s *server) patchMany(w http.ResponseWriter, r *http.Request) {
	if s.refuseParams(w, r, "id[]") {
		return
	}
	ids := r.URL.Query()["id[]"]
	if len(ids) == 0 || len(ids) > maxPatchIDs {
		s.writeJSON(w, r, http.StatusBadRequest, parsehttp.Body{
			Code:    codeBadRequest,
			Message: fmt.Sprintf("a batch patch names 1 to %d id[] keys, got %d", maxPatchIDs, len(ids)),
		})
		return
	}
	keys := make([]any, len(ids))
	for i, id := range ids {
		keys[i] = id
	}
	items, err := s.update(w, r, parse.ByIDs(keys...))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res := patchResult{Updated: items}
	if res.Updated == nil {
		res.Updated = []Product{}
	}
	updated := make(map[string]bool, len(items))
	for _, p := range items {
		updated[p.ID] = true
	}
	for _, id := range ids {
		if !updated[id] {
			updated[id] = true // a key named twice fails once
			res.Failed = append(res.Failed, itemError{ID: id, Code: codeNotFound, Error: "product " + id + " not found"})
		}
	}
	s.writeJSON(w, r, http.StatusOK, res)
}

// refuseParams answers 400 for a query parameter the route does not read,
// and reports whether it did. A patch that ignored one, a filter meant to
// narrow it, would write rows the client did not mean to.
func (s *server) refuseParams(w http.ResponseWriter, r *http.Request, accepted ...string) bool {
	q := r.URL.Query()
	names := slices.Sorted(maps.Keys(q))
	i := slices.IndexFunc(names, func(name string) bool { return !slices.Contains(accepted, name) })
	if i < 0 {
		return false
	}
	s.writeJSON(w, r, http.StatusBadRequest, parsehttp.Body{
		Code:    codeBadRequest,
		Message: fmt.Sprintf("%s %s reads no %q parameter", r.Method, r.URL.Path, names[i]),
	})
	return true
}

// update applies the request's merge patch to target's products and returns
// the rows the target matched, as the update left them: those it wrote, and
// those a patch of what they already hold or an empty patch left.
func (s *server) update(w http.ResponseWriter, r *http.Request, target parse.Target) ([]Product, error) {
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	body, err := readBody(w, r)
	if err != nil {
		return nil, err
	}
	parsed, err := s.products.ParseMergePatch(body)
	if err != nil {
		return nil, err
	}
	plan, err := s.products.BuildUpdate(filter.SQLiteDialect{}, parsed, target, parse.Scope{})
	if err != nil {
		return nil, err
	}
	res, err := parsesql.Update(ctx, s.db, s.products, plan)
	if err != nil {
		return nil, err
	}
	return slices.Concat(res.Changed, res.Unchanged), nil
}

// fail answers a parse error with its E-code and the status the code
// carries; anything else is logged and answered as a 500 without internals.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, r, err, s.report) {
		return
	}
	s.log.ErrorContext(r.Context(), "request failed", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	s.writeJSON(w, r, http.StatusInternalServerError, parsehttp.Body{Code: codeInternal, Message: "internal error"})
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

const maxBody = 64 << 10

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return nil, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodeInvalidJSON.String(), Message: "reading body: " + err.Error()}
	}
	return body, nil
}

// writeJSON answers v, or 500 when v cannot be encoded: parsehttp.WriteJSON
// writes nothing until the whole body is encoded.
func (s *server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	err := parsehttp.WriteJSON(w, r, status, v, s.report)
	if err == nil {
		return
	}
	s.log.ErrorContext(r.Context(), "encoding response", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	// A Body without details always encodes.
	_ = parsehttp.WriteJSON(w, r, http.StatusInternalServerError, parsehttp.Body{Code: codeInternal, Message: "internal error"}, s.report)
}
