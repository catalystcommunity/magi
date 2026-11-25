package compose

import (
	"context"
	"errors"
	"testing"

	releaseapi "github.com/catalystcommunity/magi/primitives/release/goapi"
)

// incidentRelease is a release controller with incidents.
type incidentRelease struct {
	releaseapi.ReleaseService
	incidents []releaseapi.DatabaseIncident
}

func (f *incidentRelease) ListIncidents(_ context.Context, r releaseapi.ListIncidentsRequest) (releaseapi.ListIncidentsResponse, error) {
	out := releaseapi.ListIncidentsResponse{Incidents: []releaseapi.DatabaseIncident{}}
	for _, i := range f.incidents {
		// Two for each call, so that the reader must ask again.
		if i.Seq > deref(r.After) && len(out.Incidents) < 2 {
			out.Incidents = append(out.Incidents, i)
		}
	}
	return out, nil
}

// Magi reads each incident of the database watch through the transport
// and tells each admin one time, also after a failure.
func TestDatabaseIncidentsGoToTheAdmins(t *testing.T) {
	now := nowMs()
	rel := &incidentRelease{incidents: []releaseapi.DatabaseIncident{
		{Seq: 1, Kind: "failover", Instance: "db-2", Message: "old news", AtMs: now - uint64(incidentAge.Milliseconds()) - 1000},
		{Seq: 2, Kind: "failover", Instance: "db-2", Message: "The primary of the database changed from db-1 to db-2.", AtMs: now - 5000},
		{Seq: 3, Kind: "restarted", Instance: "db-1", Message: "Database instance db-1 was not ready for 3 minutes. The controller restarted it.", AtMs: now - 4000},
		{Seq: 4, Kind: "recovered", Instance: "db-1", Message: "Database instance db-1 is ready again.", AtMs: now - 3000},
	}}
	admins := []string{"user:admin@example.local", "user:second@example.local"}
	sys, _, _ := adminEnv(t, Config{Admins: admins}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"ReleaseService": releaseapi.ReleaseServiceOps(rel),
	})
	var told []string
	fail := true
	tell := func(_ context.Context, admin string, inc releaseapi.DatabaseIncident) error {
		// The database is down at the first try of the third incident.
		if inc.Seq == 3 && fail {
			return errors.New("post: the database is not there")
		}
		told = append(told, admin+" "+inc.Kind)
		return nil
	}
	ctx := context.Background()
	after := sys.databaseIncidents(ctx, 0, tell)
	if after != 2 || len(told) != 2 {
		t.Fatalf("after %d, told %q: want the failover for two admins", after, told)
	}
	fail = false
	after = sys.databaseIncidents(ctx, after, tell)
	want := []string{admins[0] + " failover", admins[1] + " failover", admins[0] + " restarted", admins[1] + " restarted", admins[0] + " recovered", admins[1] + " recovered"}
	if after != 4 || len(told) != len(want) {
		t.Fatalf("after %d, told %q", after, told)
	}
	for i := range want {
		if told[i] != want[i] {
			t.Fatalf("told %q, want %q", told, want)
		}
	}
	if again := sys.databaseIncidents(ctx, after, tell); again != 4 || len(told) != len(want) {
		t.Fatalf("no news: after %d, told %q", again, told)
	}
	for kind := range incidentNotices {
		if n := incidentNotices[kind]; n.title == "" || n.urgency == "" {
			t.Fatalf("kind %s has no notice", kind)
		}
	}
}
