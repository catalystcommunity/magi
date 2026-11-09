package compose

// Connection reminders: a LinkKeys act-as grant renews for at most about 31
// days after the person consents (the renewal window of the home domain).
// Before the end, Magi tells the owner to connect again.

import (
	"context"
	"fmt"
	"sync"
	"time"

	identityapi "github.com/catalystcommunity/magi/primitives/identity/api"
)

const (
	grantRemindWithin = 3 * 24 * time.Hour
	grantRemindEvery  = 6 * time.Hour
)

// grantReminders sends at most one reminder a day for each grant that ends
// within grantRemindWithin.
func (s *System) grantReminders(ctx context.Context) {
	var mu sync.Mutex
	sent := map[string]string{} // grant id -> day of the last reminder
	t := time.NewTicker(grantRemindEvery)
	defer t.Stop()
	for {
		r, err := s.Identity.ExpiringGrants(s.AsService(ctx), identityapi.ExpiringGrantsRequest{WithinMs: uint64(grantRemindWithin / time.Millisecond)})
		if err != nil {
			s.Log.Warn("grant reminders", "err", err)
		}
		day := time.Now().In(s.zone()).Format("2006-01-02")
		for _, g := range r.Grants {
			mu.Lock()
			done := sent[g.GrantId] == day
			sent[g.GrantId] = day
			mu.Unlock()
			if done || s.renewedGrant(ctx, g) {
				continue
			}
			if _, err := s.sendNotice(ctx, grantReminder(g, s.zone())); err != nil {
				s.Log.Warn("grant reminder", "owner", g.Owner, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func grantReminder(g identityapi.Grant, loc *time.Location) noticeReq {
	what := g.Audience.AppId
	if what == "catalystcal" {
		what = "calendar"
	}
	end := time.UnixMilli(int64(g.RenewableUntilMs)).In(loc).Format("Monday 2 January at 15:04")
	return noticeReq{owner: g.Owner, title: "Connect your " + what + " again",
		text:    fmt.Sprintf("Magi's access to your %s ends on %s. To keep it, say \"connect my %s\" and approve the link.", what, end, what),
		urgency: "normal"}
}

// renewedGrant: the owner connected again, so a newer grant for the same
// application lasts longer than g.
func (s *System) renewedGrant(ctx context.Context, g identityapi.Grant) bool {
	l, err := s.Identity.ListGrants(s.AsService(ctx), identityapi.ListGrantsRequest{Owner: &g.Owner, Audience: &g.Audience})
	if err != nil {
		return false
	}
	for _, o := range l.Grants {
		if o.GrantId != g.GrantId && o.State == "active" && o.RenewableUntilMs > g.RenewableUntilMs+uint64(grantRemindWithin/time.Millisecond) {
			return true
		}
	}
	return false
}
