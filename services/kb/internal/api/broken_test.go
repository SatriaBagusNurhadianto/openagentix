package api

import (
	"context"
	"errors"

	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

type brokenStore struct{ *memory.Store }

func (brokenStore) ListCollections(context.Context) ([]store.CollectionInfo, error) {
	return nil, errors.New("connection to postgres://u:secret-dsn@db failed")
}
