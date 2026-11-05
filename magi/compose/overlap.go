package compose

import (
	"context"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

func (m *Service) CheckOverlap(ctx context.Context, req magiapi.CheckOverlapRequest) (magiapi.CheckOverlapResponse, error) {
	return magiapi.CheckOverlapResponse{}, rpc.Unavailable("check-overlap is not ready")
}
