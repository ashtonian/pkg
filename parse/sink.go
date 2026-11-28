package parse

import (
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// The sinks below are what a resource hands its provider's executor with
// each request: they scan the rows the executor reads into T and build the
// result, returning what the executor needs next (the page's keys, the
// split of an update's targets).

// listSink builds a list's page (backend.ListSink).
type listSink[T any] struct {
	r    *Resource[T]
	list backend.ListRequest

	items []T
	// total is the population's size, once Count has kept it.
	total    *int64
	facets   []filter.FacetResult
	includes []pageInclude
	// built is the page, once Keys or result has built it from items.
	built *filter.Page[T]
}

// pageInclude is one include's related keys, attached to the page in the
// order the executor read them.
type pageInclude struct {
	field string
	rows  filter.IncludeRows
}

func (s *listSink[T]) Page(rows backend.Rows) error {
	items, err := s.r.scanRows(rows)
	s.items = items
	return err
}

func (s *listSink[T]) Count(n int64) { s.total = &n }

func (s *listSink[T]) Facets(results []filter.FacetResult) { s.facets = results }

func (s *listSink[T]) Keys() ([]core.Key, error) {
	page, err := s.page()
	if err != nil {
		return nil, err
	}
	if len(s.list.Include) == 0 || len(page.Items) == 0 {
		return nil, nil
	}
	keys, err := s.r.keys(page.Items)
	if err != nil {
		return nil, err
	}
	return s.r.ownerKeys(keys)
}

func (s *listSink[T]) Include(field string, rows filter.IncludeRows) {
	s.includes = append(s.includes, pageInclude{field: field, rows: rows})
}

// page is the page built from the rows Page scanned, truncated to the
// list's limit and in presentation order (Page).
func (s *listSink[T]) page() (*filter.Page[T], error) {
	if s.built != nil {
		return s.built, nil
	}
	page, err := s.r.page(s.items, s.list.List.Limit(), s.list.List.Plan())
	if err != nil {
		return nil, err
	}
	s.built = page
	return page, nil
}

// result is the list's page with its total when the list counted, its
// facet counts and its includes.
func (s *listSink[T]) result() (*filter.Page[T], error) {
	page, err := s.page()
	if err != nil {
		return nil, err
	}
	if s.total != nil {
		page.PageInfo.TotalCount = s.total
	}
	page.Facets = filter.FacetMap(s.facets)
	for _, include := range s.includes {
		page.SetInclude(include.field, include.rows)
	}
	return page, nil
}

// querySink keeps a Query's rows (backend.QuerySink).
type querySink[T any] struct {
	r    *Resource[T]
	rows []T
}

func (s *querySink[T]) Rows(rows backend.Rows) error {
	items, err := s.r.scanRows(rows)
	s.rows = items
	return err
}

// updateSink splits an update's target rows (backend.UpdateSink).
type updateSink[T any] struct {
	r *Resource[T]
	// targets are the rows Targets read, every target as it stood, once
	// targetsRead.
	targets     []T
	targetsRead bool
	changed     []T
	unchanged   []T
	relations   int64
}

func (s *updateSink[T]) Targets(rows backend.Rows) error {
	items, err := s.r.scanRows(rows)
	s.targets, s.targetsRead = items, true
	return err
}

func (s *updateSink[T]) Changed(rows backend.Rows) error {
	items, err := s.r.scanRows(rows)
	s.changed = items
	return err
}

func (s *updateSink[T]) Unchanged(rows backend.Rows) error {
	items, err := s.r.scanRows(rows)
	s.unchanged = items
	return err
}

// Split adds to the unchanged rows the targets whose keys no changed row
// holds (unchangedRows): the rows a SkipUnchanged update matched and left
// as they were.
func (s *updateSink[T]) Split() error {
	if !s.targetsRead {
		return nil
	}
	left, err := s.r.unchangedRows(s.changed, s.targets)
	if err != nil {
		return err
	}
	s.unchanged = append(s.unchanged, left...)
	s.targets, s.targetsRead = nil, false
	return nil
}

func (s *updateSink[T]) Keys() (changed, unchanged []core.Key, err error) {
	keys, err := s.r.keys(slices.Concat(s.changed, s.unchanged))
	if err != nil || len(keys) == 0 {
		return nil, nil, err
	}
	owners, err := s.r.ownerKeys(keys)
	if err != nil {
		return nil, nil, err
	}
	return owners[:len(s.changed)], owners[len(s.changed):], nil
}

func (s *updateSink[T]) RelationRows(n int64) { s.relations += n }

func (s *updateSink[T]) result() UpdateResult[T] {
	res := UpdateResult[T]{Changed: s.changed, Unchanged: s.unchanged, RelationRows: s.relations}
	return res
}

// batchSink splits an update batch's rows, part by part
// (backend.BatchSink).
type batchSink[T any] struct {
	r     *Resource[T]
	parts []batchPart[T]
	res   UpdateResult[T]
}

// batchPart is one part of a batch: its keys, its targets as they stood,
// and the rows it wrote.
type batchPart[T any] struct {
	keys    []Key
	targets []T
	changed []T
}

func (s *batchSink[T]) Targets(keys []core.Key, rows backend.Rows) error {
	items, err := s.r.scanRows(rows)
	s.parts = append(s.parts, batchPart[T]{keys: keys, targets: items})
	return err
}

// Check verifies each part's targets against its keys (checkTargets), so
// a batch with a missing target writes nothing.
func (s *batchSink[T]) Check() error {
	kc := s.r.keyColumns()
	for _, part := range s.parts {
		keys, err := s.r.keys(part.targets)
		if err != nil {
			return err
		}
		err = kc.checkTargets(part.keys, keys)
		if err != nil {
			return fmt.Errorf("parse: %s: %w", s.r.table, err)
		}
	}
	return nil
}

func (s *batchSink[T]) Changed(part int, rows backend.Rows) error {
	if part < 0 || part >= len(s.parts) {
		return fmt.Errorf("parse: %s: update batch part %d of %d", s.r.table, part, len(s.parts))
	}
	items, err := s.r.scanRows(rows)
	s.parts[part].changed = items
	return err
}

// Split builds the result: as Changed the rows each part wrote, part by
// part, and as Unchanged the targets each part's writes left.
func (s *batchSink[T]) Split() error {
	var res UpdateResult[T]
	for _, part := range s.parts {
		unchanged, err := s.r.unchangedRows(part.changed, part.targets)
		if err != nil {
			return err
		}
		res.Changed = append(res.Changed, part.changed...)
		res.Unchanged = append(res.Unchanged, unchanged...)
	}
	s.res = res
	return nil
}
