// Command magi-world puts the synthetic world of evals/world into catalystcal
// and Longhouse, through Magi's connections (the act-as grants and the
// Longhouse session of each person). It is a test tool for a development
// installation: it needs Magi's service token (identity:present).
//
//	magi-world -world evals/world/world-v1 -identity https://magi.example.local \
//	  -token-file token-magi -me user:<uuid>@example.local -alex user:<uuid>@example.local \
//	  -calendar https://cal.example.local -longhouse https://longhouse.example.local
//
// It is safe to run again: events use create-event-once with fixed keys,
// and projects and tasks with a known name are kept.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	cal "github.com/catalystcommunity/catalystcal/clients/go"
	lh "github.com/catalystcommunity/longhouse/clients/go"

	"github.com/catalystcommunity/magi/magi/compose"
)

type event struct {
	Title       string `json:"title"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Minutes     int    `json:"minutes"`
	AllDay      bool   `json:"all_day"`
	Rrule       string `json:"rrule"`
	Location    string `json:"location"`
	Description string `json:"description"`
	Calendar    string `json:"calendar"`
}

type task struct {
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Due         string   `json:"due"`
	Who         string   `json:"who"`
	Description string   `json:"description"`
	Comments    []string `json:"comments"`
}

type house struct {
	House    string `json:"house"`
	Projects []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Tasks       []task `json:"tasks"`
	} `json:"projects"`
	Loose []task `json:"loose"`
}

var loc *time.Location

func main() {
	world := flag.String("world", "", "world directory (calendar.json, longhouse.json)")
	identity := flag.String("identity", "", "Magi base URL (identity service)")
	tokenFile := flag.String("token-file", "", "Magi service token file (identity:present)")
	me := flag.String("me", "", "subject of the person")
	alex := flag.String("alex", "", "subject of the partner")
	calURL := flag.String("calendar", "", "catalystcal URL")
	lhURL := flag.String("longhouse", "", "Longhouse URL")
	only := flag.String("only", "", "calendar or longhouse (default both)")
	clean := flag.Bool("clean", false, "remove the events and tasks that are not in the world (made by earlier runs), then stop")
	tz := flag.String("tz", "America/Denver", "time zone of the world")
	flag.Parse()
	var err error
	if loc, err = time.LoadLocation(*tz); err != nil {
		fail(err)
	}
	tok, err := os.ReadFile(*tokenFile)
	if err != nil {
		fail(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sys := compose.New(compose.Config{Identity: *identity, CatalystcalURL: *calURL, LonghouseURL: *lhURL, ServiceToken: strings.TrimSpace(string(tok))}, nil, log)
	ctx := context.Background()
	if *clean {
		var c struct{ Me, Alex []event }
		read(filepath.Join(*world, "calendar.json"), &c)
		var h house
		read(filepath.Join(*world, "longhouse.json"), &h)
		titles := map[string]bool{}
		for _, e := range append(c.Me, c.Alex...) {
			titles[e.Title] = true
		}
		cleanCalendar(ctx, sys, *me, titles)
		cleanCalendar(ctx, sys, *alex, titles)
		tasks := map[string]bool{}
		for _, p := range h.Projects {
			for _, t := range p.Tasks {
				tasks[t.Title] = true
			}
		}
		for _, t := range h.Loose {
			tasks[t.Title] = true
		}
		cleanLonghouse(ctx, sys, *me, h.House, tasks)
		return
	}
	if *only == "" || *only == "calendar" {
		var c struct{ Me, Alex []event }
		read(filepath.Join(*world, "calendar.json"), &c)
		meCal := seedCalendar(ctx, sys, *me, c.Me)
		alexCal := seedCalendar(ctx, sys, *alex, c.Alex)
		share(ctx, sys, *alex, alexCal["Alex"], *me)
		share(ctx, sys, *me, meCal["Family"], *alex)
	}
	if *only == "" || *only == "longhouse" {
		var h house
		read(filepath.Join(*world, "longhouse.json"), &h)
		seedLonghouse(ctx, sys, *me, *alex, h)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "magi-world:", err)
	os.Exit(1)
}

func read(path string, v any) {
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		fail(fmt.Errorf("%s: %w", path, err))
	}
}

func split(subject string) (string, string) {
	u, d, _ := strings.Cut(strings.TrimPrefix(subject, "user:"), "@")
	return u, d
}

// seedCalendar finds or makes the calendars that the events name and puts
// the events there. It returns the calendar ids by name ("main" is the
// default calendar; another name is the default when the server refuses a
// new calendar).
func seedCalendar(ctx context.Context, sys *compose.System, owner string, events []event) map[string]cal.CalendarID {
	tr, err := sys.CalendarFor(ctx, owner)
	if err != nil {
		fail(err)
	}
	cc := cal.NewCalendarClient(tr)
	list, err := cc.ListCalendars(ctx, cal.EmptyRequest{})
	if err != nil {
		fail(fmt.Errorf("%s: list calendars: %w", owner, err))
	}
	ids := map[string]cal.CalendarID{}
	for _, c := range list.Calendars {
		fmt.Printf("%s: calendar %q access %s path %s default %v\n", owner, c.Name, c.Access, c.AccessPath, c.IsDefault)
		if c.AccessPath != "owner" && c.AccessPath != "delegation" {
			continue
		}
		ids[c.Name] = c.CalendarId
		if c.IsDefault {
			ids["main"] = c.CalendarId
		}
	}
	for _, e := range events {
		if _, ok := ids[e.Calendar]; ok {
			continue
		}
		c, err := cc.CreateCalendar(ctx, cal.CreateCalendarRequest{Name: e.Calendar, Timezone: ptr(loc.String())})
		if err != nil {
			fmt.Printf("%s: calendar %q not made (%v); the events go on the default calendar\n", owner, e.Calendar, err)
			ids[e.Calendar] = ids["main"]
			continue
		}
		ids[e.Calendar] = c.CalendarId
		fmt.Printf("%s: made calendar %q\n", owner, e.Calendar)
	}
	ev := cal.NewEventClient(tr)
	made, kept := 0, 0
	for i, e := range events {
		x, err := toEvent(e, ids[e.Calendar])
		if err != nil {
			fail(fmt.Errorf("event %q: %w", e.Title, err))
		}
		r, err := ev.CreateEventOnce(ctx, cal.CreateEventOnceRequest{IdempotencyKey: fmt.Sprintf("world-v1-%d", i), Event: x})
		if err != nil {
			fail(fmt.Errorf("event %q: %w", e.Title, err))
		}
		if r.Created {
			made++
		} else {
			kept++
		}
	}
	fmt.Printf("%s: %d events made, %d already there\n", owner, made, kept)
	return ids
}

func toEvent(e event, calendar cal.CalendarID) (cal.Event, error) {
	x := cal.Event{CalendarId: calendar, Title: e.Title, Timezone: ptr(loc.String())}
	if e.Location != "" {
		x.Location = &e.Location
	}
	if e.Description != "" {
		x.Description = &e.Description
	}
	if e.Rrule != "" {
		x.Recurrence = &cal.Recurrence{Rrule: e.Rrule}
	}
	if e.AllDay {
		s, err := time.ParseInLocation("2006-01-02", e.Start, loc)
		if err != nil {
			return x, err
		}
		end := s.AddDate(0, 0, 1)
		if e.End != "" {
			t, err := time.ParseInLocation("2006-01-02", e.End, loc)
			if err != nil {
				return x, err
			}
			end = t.AddDate(0, 0, 1)
		}
		x.AllDay = ptr(true)
		x.StartDate, x.EndDate = ptr(cal.DateText(s.Format("2006-01-02"))), ptr(cal.DateText(end.Format("2006-01-02")))
		return x, nil
	}
	s, err := time.ParseInLocation("2006-01-02 15:04", e.Start, loc)
	if err != nil {
		return x, err
	}
	end := s.Add(time.Duration(max(e.Minutes, 15)) * time.Minute)
	if e.End != "" {
		if end, err = time.ParseInLocation("2006-01-02 15:04", e.End, loc); err != nil {
			return x, err
		}
	}
	x.Start, x.End = ptr(cal.Timestamp(s.UTC().Format(time.RFC3339))), ptr(cal.Timestamp(end.UTC().Format(time.RFC3339)))
	return x, nil
}

func share(ctx context.Context, sys *compose.System, owner string, calendar cal.CalendarID, with string) {
	if calendar == "" {
		return
	}
	tr, err := sys.CalendarFor(ctx, owner)
	if err != nil {
		fail(err)
	}
	u, d := split(with)
	_, err = cal.NewShareClient(tr).PutShare(ctx, cal.Share{CalendarId: calendar, Grantee: &cal.IdentityRef{Domain: d, UserId: &u}, Role: "write"})
	if err != nil {
		fmt.Printf("%s: share of %s with %s not made: %v (share it in the catalystcal web app)\n", owner, calendar, with, err)
		return
	}
	fmt.Printf("%s: shared %s with %s (write)\n", owner, calendar, with)
}

func seedLonghouse(ctx context.Context, sys *compose.System, me, alex string, h house) {
	tr, err := sys.LonghouseFor(me)
	if err != nil {
		fail(err)
	}
	who, err := lh.NewAuthClient(tr).Me(ctx, lh.EmptyRequest{})
	if err != nil {
		fail(fmt.Errorf("longhouse me: %w", err))
	}
	var hs *lh.HouseSummary
	for i := range who.Houses {
		if who.Houses[i].Name == h.House {
			hs = &who.Houses[i]
		}
	}
	if hs == nil {
		fail(fmt.Errorf("the person is not in the house %q", h.House))
	}
	lim := uint64(500)
	members, err := lh.NewMemberClient(tr).ListMembers(ctx, lh.HouseScopedListRequest{HouseId: hs.HouseId, Limit: &lim})
	if err != nil {
		fail(err)
	}
	alexID, _ := split(alex)
	memberOf := map[string]lh.MemberID{"me": hs.MemberId}
	for _, m := range members {
		if strings.EqualFold(m.LinkkeysUserId, alexID) {
			memberOf["alex"] = m.MemberId
		}
	}
	if memberOf["alex"] == "" {
		fail(errors.New("Alex is not a member of the house"))
	}
	pc, tc, cc := lh.NewProjectClient(tr), lh.NewTaskClient(tr), lh.NewCommentClient(tr)
	pl, err := pc.ListProjects(ctx, lh.HouseScopedListRequest{HouseId: hs.HouseId, Limit: &lim})
	if err != nil {
		fail(err)
	}
	existing := map[string]lh.ProjectID{}
	for _, p := range pl.Projects {
		existing[p.Name] = p.ProjectId
	}
	all, err := tc.ListTasks(ctx, lh.TaskListRequest{HouseId: hs.HouseId, Limit: &lim})
	if err != nil {
		fail(err)
	}
	have := map[string]bool{}
	for _, t := range all.Tasks {
		have[t.Title] = true
	}
	add := func(t task, project lh.ProjectID) {
		if have[t.Title] {
			return
		}
		x := lh.Task{HouseId: hs.HouseId, OwnerMemberId: hs.MemberId, Assignees: []lh.MemberID{memberOf[t.Who]}, Title: t.Title}
		if t.Description != "" {
			x.Description = &t.Description
		}
		if t.Due != "" {
			d, err := time.ParseInLocation("2006-01-02", t.Due, loc)
			if err != nil {
				fail(err)
			}
			x.DueAt = ptr(lh.Timestamp(d.Add(17 * time.Hour).UTC().Format(time.RFC3339)))
		}
		got, err := tc.CreateTask(ctx, x)
		if err != nil {
			fail(fmt.Errorf("task %q: %w", t.Title, err))
		}
		if project != "" {
			if _, err := pc.AddProjectTask(ctx, lh.ProjectTaskOrderRequest{ProjectId: project, TaskId: got.TaskId}); err != nil {
				fail(fmt.Errorf("task %q in project: %w", t.Title, err))
			}
		}
		if t.Status != "" && t.Status != "open" {
			got.Status = ptr(lh.TaskStatus(t.Status))
			if _, err := tc.UpdateTask(ctx, got); err != nil {
				fail(fmt.Errorf("task %q status: %w", t.Title, err))
			}
		}
		for _, c := range t.Comments {
			if _, err := cc.CreateComment(ctx, lh.Comment{HouseId: hs.HouseId, TargetType: "task", TargetId: string(got.TaskId), Body: c}); err != nil {
				fail(fmt.Errorf("comment on %q: %w", t.Title, err))
			}
		}
		fmt.Printf("longhouse: task %q\n", t.Title)
	}
	for _, p := range h.Projects {
		id, ok := existing[p.Name]
		if !ok {
			got, err := pc.CreateProject(ctx, lh.Project{HouseId: hs.HouseId, Name: p.Name, Description: ptr(p.Description)})
			if err != nil {
				fail(fmt.Errorf("project %q: %w", p.Name, err))
			}
			id = got.ProjectId
			fmt.Printf("longhouse: project %q\n", p.Name)
		}
		for _, t := range p.Tasks {
			add(t, id)
		}
	}
	for _, t := range h.Loose {
		add(t, "")
	}
}

// cleanCalendar deletes the events of the owner's own calendars that have a
// title that the world does not have.
func cleanCalendar(ctx context.Context, sys *compose.System, owner string, titles map[string]bool) {
	tr, err := sys.CalendarFor(ctx, owner)
	if err != nil {
		fail(err)
	}
	cals, err := cal.NewCalendarClient(tr).ListCalendars(ctx, cal.EmptyRequest{})
	if err != nil {
		fail(err)
	}
	var own []cal.CalendarID
	for _, c := range cals.Calendars {
		if c.AccessPath == "owner" || c.AccessPath == "delegation" {
			own = append(own, c.CalendarId)
		}
	}
	ev := cal.NewEventClient(tr)
	done := map[cal.EventID]bool{}
	for y := 2023; y <= 2028; y++ {
		from := time.Date(y, 1, 1, 0, 0, 0, 0, loc)
		l, err := ev.ListEvents(ctx, cal.ListEventsRequest{RangeStart: cal.Timestamp(from.UTC().Format(time.RFC3339)), RangeEnd: cal.Timestamp(from.AddDate(1, 0, 0).UTC().Format(time.RFC3339)), CalendarIds: own})
		if err != nil {
			fail(err)
		}
		for _, o := range l.Occurrences {
			id := o.Event.EventId
			if o.Event.MasterEventId != nil {
				id = *o.Event.MasterEventId
			}
			if titles[o.Event.Title] || done[id] {
				continue
			}
			done[id] = true
			all := cal.RecurrenceScope("all")
			if _, err := ev.DeleteEvent(ctx, cal.DeleteEventRequest{EventId: id, Scope: &all}); err != nil {
				fmt.Printf("%s: delete %q: %v\n", owner, o.Event.Title, err)
				continue
			}
			fmt.Printf("%s: deleted %q\n", owner, o.Event.Title)
		}
	}
}

// cleanLonghouse deletes the tasks of the house that the world does not have.
func cleanLonghouse(ctx context.Context, sys *compose.System, me, house string, titles map[string]bool) {
	tr, err := sys.LonghouseFor(me)
	if err != nil {
		fail(err)
	}
	who, err := lh.NewAuthClient(tr).Me(ctx, lh.EmptyRequest{})
	if err != nil {
		fail(err)
	}
	for _, hs := range who.Houses {
		if hs.Name != house {
			continue
		}
		lim := uint64(500)
		l, err := lh.NewTaskClient(tr).ListTasks(ctx, lh.TaskListRequest{HouseId: hs.HouseId, Limit: &lim})
		if err != nil {
			fail(err)
		}
		for _, t := range l.Tasks {
			if titles[t.Title] || t.OwnerMemberId != hs.MemberId {
				continue
			}
			if _, err := lh.NewTaskClient(tr).DeleteTask(ctx, t.TaskId); err != nil {
				fmt.Printf("longhouse: delete %q: %v\n", t.Title, err)
				continue
			}
			fmt.Printf("longhouse: deleted %q\n", t.Title)
		}
	}
}

func ptr[T any](v T) *T { return &v }
