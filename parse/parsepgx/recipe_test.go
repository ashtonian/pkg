//go:build unit || integration

package parsepgx_test

// The README's pgx recipe: one tenant's list and update endpoints on
// net/http. Both lanes compile it; TestRecipe serves it against PostgreSQL.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"

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
	req, err := parsehttp.ParseList(r, devices)
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
	p, err := parsehttp.ParsePatch(r, devices)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	plan, err := devices.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID(id), parse.Scope{Tenant: tenant.String()})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := parsepgx.Update(r.Context(), a.db, devices, plan)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rows := slices.Concat(res.Changed, res.Unchanged)
	if len(rows) == 0 { // not the tenant's, or no such device
		http.NotFound(w, r)
		return
	}
	a.reply(w, r, rows[0])
}

// fail answers a request's parse error with its code and status; anything
// else is the server's.
func (a *deviceAPI) fail(w http.ResponseWriter, r *http.Request, err error) {
	if parsehttp.WriteError(w, r, err, a.report()) {
		return
	}
	if _, ok := errors.AsType[*parse.BodyReadError](err); ok {
		// The client went away or sent less than it declared.
		a.log.WarnContext(r.Context(), "reading devices request body", slog.String("path", r.URL.Path), slog.Any("error", err))
		http.Error(w, "the request body could not be read", http.StatusBadRequest)
		return
	}
	a.log.ErrorContext(r.Context(), "devices request failed",
		slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Any("error", err))
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// reply encodes v whole before writing it, so a value that cannot be
// encoded is still answered with a status of its own.
func (a *deviceAPI) reply(w http.ResponseWriter, r *http.Request, v any) {
	if err := parsehttp.WriteJSON(w, r, http.StatusOK, v, a.report()); err != nil {
		a.log.ErrorContext(r.Context(), "encoding devices response", slog.String("path", r.URL.Path), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// report logs what a response could not carry: details that would not
// encode are a defect; a response that could not be written was dropped.
func (a *deviceAPI) report() parsehttp.Option {
	return parsehttp.WithErrorHandler(func(r *http.Request, err error) {
		level := slog.LevelWarn
		if errors.Is(err, parsehttp.ErrUnencodableDetails) {
			level = slog.LevelError
		}
		a.log.Log(r.Context(), level, "writing devices response", slog.String("path", r.URL.Path), slog.Any("error", err))
	})
}
