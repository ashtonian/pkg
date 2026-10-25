package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
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
)

// server serves the list contract (filter.Param*): GET lists, OPTIONS
// facets, PATCH merge patches (through BuildUpdate), GET schema discovery.
type server struct {
	db             *sql.DB
	products       *parse.Resource[Product]
	log            *slog.Logger
	requestTimeout time.Duration
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
	if err := parse.Register[Product](reg); err != nil {
		return nil, fmt.Errorf("schema registry: %w", err)
	}
	s := &server{db: db, products: products, log: log, requestTimeout: requestTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/{resource}/schema", reg.SchemaHandler())
	mux.HandleFunc("GET /api/products", s.list)
	mux.HandleFunc("OPTIONS /api/products", s.facets)
	mux.HandleFunc("PATCH /api/products/{id}", s.patch)
	return mux, nil
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
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
	items, err := s.query(ctx, plan.Page.SQL, plan.Page.Args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.products.Page(items, plan)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if plan.Count != nil {
		var total int64
		if err = s.db.QueryRowContext(ctx, plan.Count.SQL, plan.Count.Args...).Scan(&total); err != nil {
			s.fail(w, r, fmt.Errorf("count products: %w", err))
			return
		}
		page.PageInfo.TotalCount = &total
	}
	if plan.Facets != nil {
		page.Facets, err = s.facetCounts(ctx, plan.Facets)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, page)
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
	writeJSON(w, http.StatusOK, map[string]any{"facets": counts})
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

func (s *server) patch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	body, err := readBody(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	parsed, err := s.products.ParseMergePatch(body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	// Product declares no relations, so the owner statement is the whole
	// update: UPDATE … RETURNING, or a scoped read for an empty patch.
	plan, err := s.products.BuildUpdate(filter.SQLiteDialect{}, parsed, parse.ByID(id), parse.Scope{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.query(ctx, plan.Owner.SQL, plan.Owner.Args...)
	if err != nil {
		s.fail(w, r, fmt.Errorf("update product %s: %w", id, err))
		return
	}
	if len(items) == 0 {
		writeJSON(w, http.StatusNotFound, parsehttp.Body{Code: "E2076", Message: "product " + id + " not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": items})
}

func (s *server) query(ctx context.Context, q string, args ...any) (_ []Product, err error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query products: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []Product
	for rows.Next() {
		var p Product
		var labels, created string
		if err = rows.Scan(&p.ID, &p.Name, &p.Category, &p.Price, &labels, &created); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		if err = json.Unmarshal([]byte(labels), &p.Labels); err != nil {
			return nil, fmt.Errorf("product %s labels: %w", p.ID, err)
		}
		if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("product %s created_at: %w", p.ID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// fail answers a parse error with its E-code and the status the code
// carries; anything else is logged and answered as a 500 without internals.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, err) {
		return
	}
	s.log.ErrorContext(r.Context(), "request failed", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	writeJSON(w, http.StatusInternalServerError, parsehttp.Body{Code: "E5001", Message: "internal error"})
}

const maxBody = 64 << 10

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return nil, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodeInvalidJSON.String(), Message: "reading body: " + err.Error()}
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is written; an encode failure can only truncate.
	_ = json.NewEncoder(w).Encode(v)
}
