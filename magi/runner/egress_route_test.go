package runner

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// Requirement 6.3: with no proxy route the sandbox fails closed, and the
// host network is only an explicit choice of the runner configuration.
func TestEgressRouteFailsClosed(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proxy := &magiapi.RunnerEgressProxy{Environment: "run_1", Credential: "c"}
	cases := []struct {
		name     string
		res      magiapi.RunnerEgressAccessResponse
		err      error
		canProxy bool
		allow    bool
		wantErr  bool // ErrNoEgressRoute
		wantNil  bool // the proxy is nil (host network)
	}{
		{"proxy", magiapi.RunnerEgressAccessResponse{Proxy: proxy}, nil, true, false, false, false},
		{"proxy, allow set", magiapi.RunnerEgressAccessResponse{Proxy: proxy}, nil, true, true, false, false},
		{"old Magi (404)", magiapi.RunnerEgressAccessResponse{}, rpc.NotFound("x"), true, false, true, false},
		{"old Magi, allow set", magiapi.RunnerEgressAccessResponse{}, rpc.NotFound("x"), true, true, false, true},
		{"no proxy at Magi", magiapi.RunnerEgressAccessResponse{}, nil, true, false, true, false},
		{"no proxy at Magi, allow set", magiapi.RunnerEgressAccessResponse{}, nil, true, true, false, true},
		{"kind cannot proxy", magiapi.RunnerEgressAccessResponse{Proxy: proxy}, nil, false, false, true, false},
		{"kind cannot proxy, allow set", magiapi.RunnerEgressAccessResponse{Proxy: proxy}, nil, false, true, false, true},
	}
	for _, c := range cases {
		p, err := egressRoute(c.res, c.err, c.canProxy, c.allow, log)
		if c.wantErr != errors.Is(err, ErrNoEgressRoute) || (!c.wantErr && err != nil) {
			t.Errorf("%s: err = %v, want ErrNoEgressRoute=%v", c.name, err, c.wantErr)
		}
		if c.wantErr && p != nil {
			t.Errorf("%s: a failed route returned a proxy", c.name)
		}
		if !c.wantErr && (p == nil) != c.wantNil {
			t.Errorf("%s: proxy = %v, want nil=%v", c.name, p, c.wantNil)
		}
	}
	// Any other error is an error, even with the host network allowed.
	if _, err := egressRoute(magiapi.RunnerEgressAccessResponse{}, rpc.Denied("no"), true, true, log); err == nil || errors.Is(err, ErrNoEgressRoute) {
		t.Errorf("a denied call must fail as itself: %v", err)
	}
}
