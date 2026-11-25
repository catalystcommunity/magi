package compose

import (
	"context"
	"fmt"
	"time"

	releaseapi "github.com/catalystcommunity/magi/primitives/release/goapi"
)

// The database watch is in the release controller: it has the access to
// the cluster, and it works while the database does not
// (primitives/release/README.md). Magi reads its incidents and tells the
// admins.

// incidentEvery is the time between two reads of the incidents.
const incidentEvery = 30 * time.Second

// incidentAge: an older incident is not news. Magi reads all incidents
// again after a start, and the key of a notice stops a second notice.
const incidentAge = 7 * 24 * time.Hour

// incidentNotices has the title and the urgency of the notice for each
// kind of incident.
var incidentNotices = map[string]struct{ title, urgency string }{
	"failover":  {"Database failover", "normal"},
	"restarted": {"Database instance restarted", "normal"},
	"unhealthy": {"Database needs a person", "high"},
	"recovered": {"Database instance ready again", "low"},
}

// DatabaseWatch tells the admins each incident of the database watch.
func (s *System) DatabaseWatch(ctx context.Context) {
	if s.Release == nil || len(s.Cfg.Admins) == 0 {
		return
	}
	t := time.NewTicker(incidentEvery)
	defer t.Stop()
	after := uint64(0)
	for {
		after = s.databaseIncidents(ctx, after, s.incidentNotice)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// databaseIncidents reads the incidents after the incident with the
// number after and calls tell for each admin. It returns the number of the
// last incident that all admins got. An incident that an admin did not get
// (the database can be the cause) is read again at the next call.
func (s *System) databaseIncidents(ctx context.Context, after uint64, tell func(ctx context.Context, admin string, inc releaseapi.DatabaseIncident) error) uint64 {
	for {
		res, err := s.Release.ListIncidents(s.AsService(ctx), releaseapi.ListIncidentsRequest{After: &after})
		if err != nil {
			s.Log.Debug("database watch: list incidents", "err", err)
			return after
		}
		if len(res.Incidents) == 0 {
			return after
		}
		for _, inc := range res.Incidents {
			if time.Since(time.UnixMilli(int64(inc.AtMs))) < incidentAge {
				s.Log.Warn("database incident", "seq", inc.Seq, "kind", inc.Kind, "instance", inc.Instance, "message", inc.Message)
				for _, admin := range s.Cfg.Admins {
					if err := tell(ctx, admin, inc); err != nil {
						s.Log.Warn("database watch: notice", "seq", inc.Seq, "err", err)
						return after
					}
				}
			}
			after = inc.Seq
		}
	}
}

// incidentNotice sends the notice of one incident to one admin.
func (s *System) incidentNotice(ctx context.Context, admin string, inc releaseapi.DatabaseIncident) error {
	n, ok := incidentNotices[inc.Kind]
	if !ok {
		n = incidentNotices["unhealthy"]
	}
	at := time.UnixMilli(int64(inc.AtMs)).In(s.zone()).Format("2 January, 15:04")
	_, err := s.sendNotice(ctx, noticeReq{owner: admin, persona: Engineering, urgency: n.urgency, title: n.title,
		text: fmt.Sprintf("%s (%s)", clip(neutralize(inc.Message), 400), at),
		key:  fmt.Sprintf("database-incident:%d:%d/%s", inc.AtMs, inc.Seq, admin)})
	return err
}
