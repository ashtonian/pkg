package parsememory

import (
	"context"
	"errors"

	"github.com/ashtonian/pkg/parse/internal/backend"
)

var errUpdatePending = errors.New("parsememory: update pending the column applier merge")

func (e executor) Update(context.Context, backend.UpdateRequest, backend.UpdateSink) error {
	return errUpdatePending
}

func (e executor) UpdateBatch(context.Context, backend.UpdateBatchRequest, backend.BatchSink) error {
	return errUpdatePending
}
