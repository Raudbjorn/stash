package api

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/scene/metadata/entity"
	"github.com/stretchr/testify/assert"
)

func TestGQLErrorHandlerAnnotatesInsufficientDisk(t *testing.T) {
	err := &entity.ErrInsufficientDisk{Required: 200, Available: 100}
	presented := gqlErrorHandler(context.Background(), err)

	assert.Equal(t, "INSUFFICIENT_DISK", presented.Extensions["code"])
	assert.Equal(t, int64(200), presented.Extensions["requiredBytes"])
	assert.Equal(t, int64(100), presented.Extensions["availableBytes"])
}
