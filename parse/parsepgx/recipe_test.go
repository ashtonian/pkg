//go:build unit || integration

package parsepgx_test

// The README's pgx recipe: one tenant's list and update endpoints on
// net/http. Both lanes compile it; TestRecipe serves it against PostgreSQL.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// Device is the recipe's resource. Its db tags drive parse's SQL, pgx's row
// scanning and the page cursors.
type Device struct {
	ID       uuid.UUID `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID uuid.UUID `json:"tenant_id" db:"tenant_id"`
	Name     string    `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Status   string    `json:"status"    db:"status"    parse:"filter,facet,patch" enum:"active,offline" facet:"terms:10"`
}

var devices = parse.MustResource[Device]("devices", parse.TenantColumn("tenant_id"), parse.PageSize(25, 100))

// deviceAPI serves the devices of the tenant a request is authenticated
// for; tenant reads it from the authentication, never from the request.
type deviceAPI struct {
	db     parsepgx.DB
	tenant func(*http.Request) (uuid.UUID, error)
	log    *slog.Logger
}

type tenantKey struct{}

// authenticatedTenant is the tenant the authentication middleware put in
// the request's context.
func authenticatedTenant(r *http.Request) (uuid.UUID, error) {
	id, ok := r.Context().Value(tenantKey{}).(uuid.UUID)
	if !ok {
		return uuid.Nil, errors.New("no authenticated tenant")
	}
	return id, nil
}

// The recipe's endpoints on a mux, behind authentication that puts the
// request's tenant in its context.
func Example() {
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("open database", slog.Any("error", err))
		return
	}
	defer pool.Close()

	api := &deviceAPI{db: pool, tenant: authenticatedTenant, log: slog.Default()}
	mux := http.NewServeMux()
	api.routes(mux)
}

func (a *deviceAPI) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /devices", a.list)
	mux.HandleFunc("PATCH /devices/{id}", a.update)
}

// list answers GET /devices?status=active&sort=name&include_total=true&facets=status.
func (a *deviceAPI) list(w http.ResponseWriter, r *http.Request) {
	tenant, err := a.tenant(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	req, err := devices.ParseList(r.URL.Query())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	plan, err := devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: tenant.String()})
	if err != nil {
		a.fail(w, r, err) // an invalid cursor is the request's error
		return
	}
	page, err := parsepgx.QueryPage(r.Context(), a.db, devices, plan)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.reply(w, r, page)
}

// update answers PATCH /devices/{id} with a JSON Merge Patch.
func (a *deviceAPI) update(w http.ResponseWriter, r *http.Request) {
	tenant, err := a.tenant(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	p, err := devices.ParseMergePatch(body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	plan, err := devices.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(id), parse.Scope{Tenant: tenant.String()})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := parsepgx.Update(r.Context(), a.db, devices, plan)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(rows) == 0 { // not the tenant's, or no such device
		http.NotFound(w, r)
		return
	}
	a.reply(w, r, rows[0])
}

// fail answers a request's parse error with its code and status; anything
// else is the server's.
func (a *deviceAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, err) {
		return
	}
	a.log.ErrorContext(r.Context(), "devices request failed",
		slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (a *deviceAPI) reply(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.log.WarnContext(r.Context(), "devices response write failed", slog.String("path", r.URL.Path), slog.Any("error", err))
	}
}
