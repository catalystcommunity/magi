package compose

// The persona tools for the calendar (catalystcal) and for projects and
// tasks (Longhouse). Each call acts as the person through connect.go.
// Times in and out are local times of the household (MAGI_TIME_ZONE).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	cal "github.com/catalystcommunity/catalystcal/clients/go"
	lh "github.com/catalystcommunity/longhouse/clients/go"

	"github.com/catalystcommunity/magi/magi/model"
)

var calendarToolNames = []string{"calendar_events", "find_events", "create_event", "change_event", "cancel_event", "free_busy"}
var longhouseToolNames = []string{"longhouse_projects", "longhouse_tasks", "add_longhouse_task", "update_longhouse_task", "add_longhouse_project"}

func workTools(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) []model.Tool {
	scope := map[string]any{"type": "string", "enum": []string{"this", "future", "all"}, "description": "For a repeating event: this occurrence, this and later ones, or all (default all)."}
	return []model.Tool{
		{Name: "connect_account", Description: "Get a link that lets the person connect their calendar or Longhouse (projects and tasks) to Magi. Give the person the link. Use it only when another tool says that the system is not connected.", Parameters: obj(map[string]any{
			"system": map[string]any{"type": "string", "enum": []string{"calendar", "longhouse"}},
		}, "system")},
		{Name: "calendar_events", Description: "List the events on the person's calendars and the calendars that others share with them, in a time range. Repeating events show each occurrence.", Parameters: obj(map[string]any{
			"from":     str("Start, YYYY-MM-DD or YYYY-MM-DD HH:MM. Default: today."),
			"to":       str("End, same format; a date alone includes that day. Default: 7 days after from. At most 400 days."),
			"text":     str("Optional words; only events whose title, place, or notes have one of them."),
			"calendar": str("Optional calendar name or owner name."),
		})},
		{Name: "find_events", Description: "Search all the person's events (past and future) by words in the title, place, or notes.", Parameters: obj(map[string]any{
			"text": str("Words to find."),
		}, "text")},
		{Name: "create_event", Description: "Put an event on a calendar. Default: the person's own main calendar. A calendar that someone shares with write access can be named.", Parameters: obj(map[string]any{
			"title":            str("Title."),
			"start":            str("YYYY-MM-DD HH:MM, or YYYY-MM-DD for an all-day event."),
			"end":              str("Optional. YYYY-MM-DD HH:MM. Default: start + duration_minutes."),
			"duration_minutes": map[string]any{"type": "integer", "description": "Optional. Default 60."},
			"repeat":           str("Optional. daily, weekdays, weekly, every 2 weeks, monthly, yearly, or an RRULE such as FREQ=WEEKLY;BYDAY=TU,TH."),
			"until":            str("Optional. Last date of a repeating event, YYYY-MM-DD."),
			"calendar":         str("Optional calendar name or owner name."),
			"location":         str("Optional place."),
			"description":      str("Optional notes."),
			"attendees":        str("Optional. Comma-separated e-mail addresses or names of people to invite."),
		}, "title", "start")},
		{Name: "change_event", Description: "Change an event: its time, title, place, or notes. Give only what changes.", Parameters: obj(map[string]any{
			"event":       str("Event id, from calendar_events or find_events."),
			"occurrence":  str("For one occurrence of a repeating event: its start as calendar_events shows it."),
			"scope":       scope,
			"title":       str("New title."),
			"start":       str("New start, YYYY-MM-DD HH:MM."),
			"end":         str("New end, YYYY-MM-DD HH:MM."),
			"location":    str("New place."),
			"description": str("New notes."),
		}, "event")},
		{Name: "cancel_event", Description: "Delete an event (it goes to the calendar trash), or one occurrence of a repeating event.", Parameters: obj(map[string]any{
			"event":      str("Event id."),
			"occurrence": str("For one occurrence: its start as calendar_events shows it."),
			"scope":      scope,
		}, "event")},
		{Name: "free_busy", Description: "When people are busy, for finding a time. People are the person's own calendar and the owners of calendars shared with them.", Parameters: obj(map[string]any{
			"people": str("Comma-separated names or LinkKeys handles (user@domain). Default: the person."),
			"from":   str("Start, YYYY-MM-DD or YYYY-MM-DD HH:MM."),
			"to":     str("End, same format. Default: from + 1 day."),
		}, "from")},
		{Name: "longhouse_projects", Description: "List the projects in the person's Longhouse houses (shared projects and tasks).", Parameters: obj(map[string]any{
			"house":    str("Optional house name."),
			"archived": map[string]any{"type": "boolean", "description": "Also archived projects."},
		})},
		{Name: "longhouse_tasks", Description: "List Longhouse tasks (shared work of a house or a project, with status, due date, and who does it). These are not the person's private to-dos.", Parameters: obj(map[string]any{
			"project": str("Optional project name."),
			"state":   map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}, "description": "Default open."},
			"mine":    map[string]any{"type": "boolean", "description": "Only tasks that the person owns or does."},
			"text":    str("Optional words in the title or description."),
			"house":   str("Optional house name."),
		})},
		{Name: "add_longhouse_task", Description: "Add a task in Longhouse, optionally in a project and for a member.", Parameters: obj(map[string]any{
			"title":       str("Title."),
			"project":     str("Optional project name."),
			"due":         str("Optional due time, YYYY-MM-DD or YYYY-MM-DD HH:MM."),
			"assignee":    str("Optional member name. Default: the person."),
			"description": str("Optional details."),
			"house":       str("Optional house name."),
		}, "title")},
		{Name: "update_longhouse_task", Description: "Change a Longhouse task: status, due date, title, details, or add a comment.", Parameters: obj(map[string]any{
			"task":        str("Task id, from longhouse_tasks."),
			"status":      map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "cancelled"}},
			"due":         str("New due time."),
			"title":       str("New title."),
			"description": str("New details."),
			"comment":     str("A comment to add."),
		}, "task")},
		{Name: "add_longhouse_project", Description: "Make a Longhouse project.", Parameters: obj(map[string]any{
			"name":        str("Name."),
			"description": str("Optional description."),
			"house":       str("Optional house name."),
		}, "name")},
	}
}

// workTool runs the calendar and Longhouse tools. ok is false for others.
func (s *System) workTool(ctx context.Context, owner, key, name string, args map[string]string) (string, bool) {
	var out string
	var err error
	switch {
	case name == "connect_account":
		r, cerr := s.connect(ctx, owner, args["system"])
		if cerr != nil {
			return "error: " + cerr.Error(), true
		}
		return fmt.Sprintf("Give the person this link: %s\n%s The link works until %s.", r.Url, r.Message, at(time.UnixMilli(int64(r.ExpiresMs)), time.Now(), s.zone())), true
	case slices.Contains(calendarToolNames, name):
		if s.Calendar == nil {
			return "error: no calendar service is configured", true
		}
		out, err = s.calendarTool(ctx, owner, key, name, args)
	case slices.Contains(longhouseToolNames, name):
		if s.Longhouse == nil {
			return "error: Longhouse is not configured", true
		}
		out, err = s.longhouseTool(ctx, owner, name, args)
	default:
		return "", false
	}
	var nc errNotConnected
	if errors.As(err, &nc) {
		return "error: " + nc.Error(), true
	}
	if err != nil {
		var ce *cal.ClientError
		if errors.As(err, &ce) && ce.Code != 0 {
			return fmt.Sprintf("error: %s (%d)", ce.Message, ce.Code), true
		}
		return "error: " + err.Error(), true
	}
	return out, true
}

// localTime parses a local "YYYY-MM-DD[ HH:MM]". date is true without a time.
func localTime(v string, loc *time.Location) (t time.Time, date bool, err error) {
	v = strings.TrimSpace(strings.Replace(v, "T", " ", 1))
	if t, err = time.ParseInLocation("2006-01-02 15:04", v, loc); err == nil {
		return t, false, nil
	}
	if t, err = time.ParseInLocation("2006-01-02 15:04:05", v, loc); err == nil {
		return t, false, nil
	}
	if t, err = time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t, true, nil
	}
	if t, err = time.Parse(time.RFC3339, v); err == nil {
		return t.In(loc), false, nil
	}
	return time.Time{}, false, fmt.Errorf("%q is not YYYY-MM-DD or YYYY-MM-DD HH:MM", v)
}

func ts(t time.Time) cal.Timestamp { return cal.Timestamp(t.UTC().Format(time.RFC3339)) }

func parseTS(v cal.Timestamp) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, string(v))
	return t
}

func (s *System) calendars(ctx context.Context, tr cal.Transport) ([]cal.Calendar, error) {
	l, err := cal.NewCalendarClient(tr).ListCalendars(ctx, cal.EmptyRequest{})
	return l.Calendars, err
}

func personName(p *cal.PersonSummary) string {
	if p == nil {
		return ""
	}
	if p.DisplayName != nil && *p.DisplayName != "" {
		return *p.DisplayName
	}
	if p.Handle != nil && *p.Handle != "" {
		return *p.Handle
	}
	return p.Subject
}

// own: a calendar of the person. Through an act-as grant the access path
// of the person's own calendars is "delegation".
func own(c cal.Calendar) bool { return c.AccessPath == "owner" || c.AccessPath == "delegation" }

func calName(c cal.Calendar) string {
	if own(c) {
		return c.Name
	}
	return c.Name + " (" + personName(&c.Owner) + ")"
}

// pickCalendar finds a calendar by name or owner. Empty: the default one.
func pickCalendar(cals []cal.Calendar, want string, write bool) (cal.Calendar, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	writable := func(c cal.Calendar) bool {
		return !write || c.Access == "owner" || c.Access == "manage" || c.Access == "write" || c.Access == "contribute"
	}
	var best []cal.Calendar
	for _, c := range cals {
		if !writable(c) {
			continue
		}
		if want == "" {
			if c.IsDefault && own(c) {
				return c, nil
			}
			continue
		}
		owner := strings.ToLower(personName(&c.Owner))
		name := strings.ToLower(c.Name)
		if name == want || owner == want || strings.Contains(name, want) || (owner != "" && strings.Contains(want, owner)) || strings.Contains(owner, strings.TrimSuffix(strings.TrimSuffix(want, "'s"), "s calendar")) {
			best = append(best, c)
		}
	}
	if len(best) == 1 {
		return best[0], nil
	}
	var names []string
	for _, c := range cals {
		if writable(c) {
			names = append(names, calName(c)+" ["+string(c.Access)+"]")
		}
	}
	if len(best) > 1 {
		return cal.Calendar{}, fmt.Errorf("more than one calendar matches %q: %s", want, strings.Join(names, "; "))
	}
	if want == "" {
		return cal.Calendar{}, errors.New("the person has no default calendar")
	}
	return cal.Calendar{}, fmt.Errorf("no calendar matches %q. Calendars: %s", want, strings.Join(names, "; "))
}

func (s *System) describeOcc(o cal.Occurrence, names map[cal.CalendarID]string) string {
	loc := s.zone()
	e := o.Event
	st, en := parseTS(o.Start).In(loc), parseTS(o.End).In(loc)
	var b strings.Builder
	if e.AllDay != nil && *e.AllDay {
		b.WriteString(st.Format("Mon 2006-01-02") + " all day")
	} else {
		b.WriteString(st.Format("Mon 2006-01-02 15:04") + "-" + en.Format("15:04"))
	}
	if o.BusyOnly {
		b.WriteString(" busy")
	} else {
		b.WriteString(" " + e.Title)
		if e.Location != nil && *e.Location != "" {
			b.WriteString(" @ " + *e.Location)
		}
	}
	if n := names[e.CalendarId]; n != "" {
		b.WriteString(" [" + n + "]")
	}
	if e.Recurrence != nil {
		b.WriteString(" (repeats " + e.Recurrence.Rrule + ")")
	}
	if len(e.Attendees) > 0 && !o.BusyOnly {
		var who []string
		for _, a := range e.Attendees {
			switch {
			case a.Person != nil:
				who = append(who, personName(a.Person))
			case a.Name != nil:
				who = append(who, *a.Name)
			case a.Email != nil:
				who = append(who, *a.Email)
			}
		}
		b.WriteString(" with " + strings.Join(who, ", "))
	}
	id := string(e.EventId)
	if e.MasterEventId != nil {
		id = string(*e.MasterEventId)
	}
	b.WriteString(" id " + id)
	if o.RecurrenceId != nil {
		b.WriteString(" occurrence " + parseTS(*o.RecurrenceId).In(loc).Format("2006-01-02 15:04"))
	}
	if !o.BusyOnly && e.Description != nil && *e.Description != "" {
		b.WriteString("\n  notes: " + clip(strings.ReplaceAll(*e.Description, "\n", " "), 200))
	}
	return b.String()
}

func anyWord(text, words string) bool {
	text = strings.ToLower(text)
	for _, w := range strings.FieldsFunc(strings.ToLower(words), func(r rune) bool { return r == ' ' || r == ',' }) {
		if len(w) > 1 && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

var rruleRE = regexp.MustCompile(`^(RRULE:)?FREQ=(DAILY|WEEKLY|MONTHLY|YEARLY)(;[A-Z]+=[A-Z0-9,+-]+)*$`)

func repeatRule(v, until string, loc *time.Location) (string, error) {
	v = strings.TrimSpace(v)
	rules := map[string]string{"daily": "FREQ=DAILY", "every day": "FREQ=DAILY", "weekdays": "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
		"weekly": "FREQ=WEEKLY", "every week": "FREQ=WEEKLY", "every 2 weeks": "FREQ=WEEKLY;INTERVAL=2", "biweekly": "FREQ=WEEKLY;INTERVAL=2",
		"every other week": "FREQ=WEEKLY;INTERVAL=2", "monthly": "FREQ=MONTHLY", "every month": "FREQ=MONTHLY", "yearly": "FREQ=YEARLY", "annually": "FREQ=YEARLY"}
	r, ok := rules[strings.ToLower(v)]
	if !ok {
		r = strings.TrimPrefix(strings.ToUpper(strings.ReplaceAll(v, " ", "")), "RRULE:")
		if !rruleRE.MatchString(r) {
			return "", fmt.Errorf("repeat %q: use daily, weekdays, weekly, every 2 weeks, monthly, yearly, or an RRULE", v)
		}
	}
	if until = strings.TrimSpace(until); until != "" && !strings.Contains(r, "UNTIL=") && !strings.Contains(r, "COUNT=") {
		u, _, err := localTime(until, loc)
		if err != nil {
			return "", fmt.Errorf("until: %v", err)
		}
		end := time.Date(u.Year(), u.Month(), u.Day(), 23, 59, 59, 0, loc)
		r += ";UNTIL=" + end.UTC().Format("20060102T150405Z")
	}
	return r, nil
}

func (s *System) calendarTool(ctx context.Context, owner, key, name string, args map[string]string) (string, error) {
	tr, err := s.Calendar.transport(ctx, owner)
	if err != nil {
		return "", err
	}
	loc, now := s.zone(), time.Now()
	ev := cal.NewEventClient(tr)
	switch name {
	case "calendar_events":
		from := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
		if v := args["from"]; v != "" {
			if from, _, err = localTime(v, loc); err != nil {
				return "", err
			}
		}
		to := from.AddDate(0, 0, 7)
		if v := args["to"]; v != "" {
			var date bool
			if to, date, err = localTime(v, loc); err != nil {
				return "", err
			}
			if date {
				// A date alone includes that day.
				to = to.AddDate(0, 0, 1)
			}
		}
		if !to.After(from) || to.Sub(from) > 400*24*time.Hour {
			return "", errors.New("to must be after from, and at most 400 days later")
		}
		cals, err := s.calendars(ctx, tr)
		if err != nil {
			return "", err
		}
		names := map[cal.CalendarID]string{}
		var ids []cal.CalendarID
		for _, c := range cals {
			names[c.CalendarId] = calName(c)
		}
		if w := args["calendar"]; w != "" {
			c, err := pickCalendar(cals, w, false)
			if err != nil {
				return "", err
			}
			ids = []cal.CalendarID{c.CalendarId}
		}
		yes := true
		l, err := ev.ListEvents(ctx, cal.ListEventsRequest{RangeStart: ts(from), RangeEnd: ts(to), CalendarIds: ids, IncludeInvitations: &yes})
		if err != nil {
			return "", err
		}
		sort.SliceStable(l.Occurrences, func(i, j int) bool { return l.Occurrences[i].Start < l.Occurrences[j].Start })
		var lines []string
		for _, o := range l.Occurrences {
			e := o.Event
			if t := args["text"]; t != "" && !anyWord(e.Title+" "+deref(e.Location)+" "+deref(e.Description), t) {
				continue
			}
			lines = append(lines, s.describeOcc(o, names))
		}
		head := fmt.Sprintf("%d events from %s to %s:", len(lines), from.Format("Mon 2006-01-02 15:04"), to.Format("Mon 2006-01-02 15:04"))
		if len(lines) > 80 {
			head += fmt.Sprintf(" (the first 80 of %d; ask for a shorter range)", len(lines))
			lines = lines[:80]
		}
		if l.Truncated {
			head += " The calendar cut the list: ask for a shorter range."
		}
		if len(lines) == 0 {
			return head + " none.", nil
		}
		return head + "\n" + strings.Join(lines, "\n"), nil
	case "find_events":
		lim := uint64(40)
		l, err := ev.SearchEvents(ctx, cal.SearchEventsRequest{Query: args["text"], Limit: &lim})
		if err != nil {
			return "", err
		}
		cals, _ := s.calendars(ctx, tr)
		names := map[cal.CalendarID]string{}
		for _, c := range cals {
			names[c.CalendarId] = calName(c)
		}
		if len(l.Events) == 0 {
			return "No events match " + args["text"] + ".", nil
		}
		sort.SliceStable(l.Events, func(i, j int) bool { return deref((*string)(l.Events[i].Start)) < deref((*string)(l.Events[j].Start)) })
		var lines []string
		for _, e := range l.Events {
			o := cal.Occurrence{Event: e, Access: "read"}
			if e.Start != nil {
				o.Start = *e.Start
			}
			if e.End != nil {
				o.End = *e.End
			}
			if e.AllDay != nil && *e.AllDay && e.StartDate != nil {
				d, _ := time.ParseInLocation("2006-01-02", string(*e.StartDate), loc)
				o.Start = ts(d)
			}
			lines = append(lines, s.describeOcc(o, names))
		}
		return fmt.Sprintf("%d events (a repeating event shows its first time):\n%s", len(lines), strings.Join(lines, "\n")), nil
	case "create_event":
		title := strings.TrimSpace(args["title"])
		if title == "" {
			return "", errors.New("give a title")
		}
		st, date, err := localTime(args["start"], loc)
		if err != nil {
			return "", fmt.Errorf("start: %v", err)
		}
		cals, err := s.calendars(ctx, tr)
		if err != nil {
			return "", err
		}
		c, err := pickCalendar(cals, args["calendar"], true)
		if err != nil {
			return "", err
		}
		e := cal.Event{CalendarId: c.CalendarId, Title: clip(title, 200), Timezone: ptr(loc.String())}
		if date {
			end := st.AddDate(0, 0, 1)
			if v := args["end"]; v != "" {
				if t, _, err := localTime(v, loc); err == nil && t.After(st) {
					end = t.AddDate(0, 0, 1)
				}
			}
			e.AllDay = ptr(true)
			e.StartDate, e.EndDate = ptr(cal.DateText(st.Format("2006-01-02"))), ptr(cal.DateText(end.Format("2006-01-02")))
		} else {
			end := st.Add(60 * time.Minute)
			if d := args["duration_minutes"]; d != "" {
				var m int
				if _, err := fmt.Sscan(d, &m); err == nil && m > 0 && m < 24*60*14 {
					end = st.Add(time.Duration(m) * time.Minute)
				}
			}
			if v := args["end"]; v != "" {
				t, _, err := localTime(v, loc)
				if err != nil || !t.After(st) {
					return "", errors.New("end must be a time after start")
				}
				end = t
			}
			e.Start, e.End = ptr(ts(st)), ptr(ts(end))
		}
		if v := args["repeat"]; v != "" && v != "none" {
			r, err := repeatRule(v, args["until"], loc)
			if err != nil {
				return "", err
			}
			e.Recurrence = &cal.Recurrence{Rrule: r}
		}
		if v := strings.TrimSpace(args["location"]); v != "" {
			e.Location = &v
		}
		if v := strings.TrimSpace(args["description"]); v != "" {
			e.Description = &v
		}
		for _, a := range strings.Split(args["attendees"], ",") {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			if strings.Contains(a, "@") && !strings.Contains(a, " ") {
				e.Attendees = append(e.Attendees, cal.Attendee{Email: ptr(a)})
			} else {
				e.Attendees = append(e.Attendees, cal.Attendee{Name: ptr(a)})
			}
		}
		h := sha256.Sum256([]byte(key + "\n" + title + "\n" + args["start"] + "\n" + string(c.CalendarId)))
		r, err := ev.CreateEventOnce(ctx, cal.CreateEventOnceRequest{IdempotencyKey: "magi-" + hex.EncodeToString(h[:12]), Event: e})
		if err != nil {
			return "", err
		}
		got := e
		if r.Event != nil {
			got = *r.Event
		}
		o := cal.Occurrence{Event: got, Access: "owner"}
		if got.Start != nil {
			o.Start, o.End = *got.Start, *got.End
		} else {
			o.Start = ts(st)
		}
		return "Created: " + s.describeOcc(o, map[cal.CalendarID]string{c.CalendarId: calName(c)}), nil
	case "change_event", "cancel_event":
		id := cal.EventID(strings.TrimSpace(args["event"]))
		if id == "" {
			return "", errors.New("give the event id")
		}
		scope := cal.RecurrenceScope("all")
		var rid *cal.Timestamp
		if v := strings.TrimSpace(args["occurrence"]); v != "" {
			t, _, err := localTime(v, loc)
			if err != nil {
				return "", fmt.Errorf("occurrence: %v", err)
			}
			rid = ptr(ts(t))
			scope = "this"
		}
		switch args["scope"] {
		case "this":
			scope = "this"
		case "future":
			scope = "this_and_future"
		case "all":
			scope = "all"
		}
		if scope != "all" && rid == nil {
			return "", errors.New("give the occurrence for scope this or future")
		}
		if scope == "all" {
			rid = nil
		}
		if name == "cancel_event" {
			if _, err := ev.DeleteEvent(ctx, cal.DeleteEventRequest{EventId: id, Scope: &scope, RecurrenceId: rid}); err != nil {
				return "", err
			}
			return "Deleted (it is in the calendar trash for 30 days).", nil
		}
		e, err := ev.GetEvent(ctx, id)
		if err != nil {
			return "", err
		}
		if v := strings.TrimSpace(args["title"]); v != "" {
			e.Title = clip(v, 200)
		}
		if v, ok := args["location"]; ok && v != "" {
			e.Location = ptr(v)
		}
		if v, ok := args["description"]; ok && v != "" {
			e.Description = ptr(v)
		}
		if v := args["start"]; v != "" && e.Start != nil {
			t, _, err := localTime(v, loc)
			if err != nil {
				return "", fmt.Errorf("start: %v", err)
			}
			dur := parseTS(*e.End).Sub(parseTS(*e.Start))
			e.Start = ptr(ts(t))
			if args["end"] == "" {
				e.End = ptr(ts(t.Add(dur)))
			}
		}
		if v := args["end"]; v != "" && e.End != nil {
			t, _, err := localTime(v, loc)
			if err != nil {
				return "", fmt.Errorf("end: %v", err)
			}
			e.End = ptr(ts(t))
		}
		got, err := ev.UpdateEvent(ctx, cal.UpdateEventRequest{Event: e, Scope: &scope, RecurrenceId: rid})
		if err != nil {
			return "", err
		}
		o := cal.Occurrence{Event: got, Access: "owner"}
		if got.Start != nil {
			o.Start, o.End = *got.Start, *got.End
		}
		return "Changed: " + s.describeOcc(o, nil), nil
	case "free_busy":
		from, _, err := localTime(args["from"], loc)
		if err != nil {
			return "", fmt.Errorf("from: %v", err)
		}
		to := from.AddDate(0, 0, 1)
		if v := args["to"]; v != "" {
			if to, _, err = localTime(v, loc); err != nil {
				return "", fmt.Errorf("to: %v", err)
			}
		}
		cals, err := s.calendars(ctx, tr)
		if err != nil {
			return "", err
		}
		var people []cal.IdentityRef
		var labels []string
		for _, w := range strings.Split(args["people"], ",") {
			w = strings.ToLower(strings.TrimSpace(w))
			if w == "" {
				continue
			}
			found := false
			for _, c := range cals {
				o := c.Owner
				if strings.Contains(strings.ToLower(personName(&o)), w) || (o.Handle != nil && strings.EqualFold(*o.Handle, w)) {
					uid := string(o.UserId)
					people = append(people, cal.IdentityRef{Domain: o.Domain, UserId: &uid})
					labels = append(labels, personName(&o))
					found = true
					break
				}
			}
			if !found && strings.Contains(w, "@") {
				h, d, _ := strings.Cut(w, "@")
				people = append(people, cal.IdentityRef{Domain: d, Handle: &h})
				labels = append(labels, w)
			}
		}
		if len(people) == 0 {
			if uid, dom, ok := userParts(owner); ok {
				people = append(people, cal.IdentityRef{Domain: dom, UserId: &uid})
				labels = append(labels, "the person")
			}
		}
		r, err := cal.NewFreeBusyClient(tr).Query(ctx, cal.FreeBusyRequest{People: people, RangeStart: ts(from), RangeEnd: ts(to)})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for i, p := range r.People {
			label := personName(p.Person)
			if label == "" && i < len(labels) {
				label = labels[i]
			}
			if !p.Visible {
				fmt.Fprintf(&b, "%s: not visible to the person\n", label)
				continue
			}
			fmt.Fprintf(&b, "%s busy:", label)
			if len(p.Busy) == 0 {
				b.WriteString(" never")
			}
			for _, iv := range p.Busy {
				b.WriteString(" " + parseTS(iv.Start).In(loc).Format("Mon 15:04") + "-" + parseTS(iv.End).In(loc).Format("15:04"))
			}
			b.WriteString("\n")
		}
		return strings.TrimSpace(b.String()), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

// userParts splits user:<uuid>@<domain>.
func userParts(subject string) (string, string, bool) {
	rest, ok := strings.CutPrefix(subject, "user:")
	if !ok {
		return "", "", false
	}
	u, d, ok := strings.Cut(rest, "@")
	return u, d, ok
}

// Longhouse.

type lhHouse struct {
	id, name, member string
	members          map[lh.MemberID]string
}

func (s *System) lhHouse(ctx context.Context, tr cal.Transport, want string) (lhHouse, error) {
	me, err := lh.NewAuthClient(tr).Me(ctx, lh.EmptyRequest{})
	if err != nil {
		return lhHouse{}, err
	}
	if len(me.Houses) == 0 {
		return lhHouse{}, errors.New("the person is in no Longhouse house")
	}
	pick := me.Houses[0]
	if want = strings.ToLower(strings.TrimSpace(want)); want != "" {
		found := false
		for _, h := range me.Houses {
			if strings.Contains(strings.ToLower(h.Name), want) {
				pick, found = h, true
				break
			}
		}
		if !found {
			var names []string
			for _, h := range me.Houses {
				names = append(names, h.Name)
			}
			return lhHouse{}, fmt.Errorf("no house matches %q. Houses: %s", want, strings.Join(names, "; "))
		}
	}
	h := lhHouse{id: string(pick.HouseId), name: pick.Name, member: string(pick.MemberId), members: map[lh.MemberID]string{}}
	lim := uint64(500)
	ms, err := lh.NewMemberClient(tr).ListMembers(ctx, lh.HouseScopedListRequest{HouseId: pick.HouseId, Limit: &lim})
	if err == nil {
		for _, m := range ms {
			n := deref(m.DisplayName)
			if n == "" {
				n = deref(m.Handle)
			}
			h.members[m.MemberId] = n
		}
	}
	return h, nil
}

func (h lhHouse) memberID(name string) (lh.MemberID, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "me" {
		return lh.MemberID(h.member), nil
	}
	var hit []lh.MemberID
	for id, n := range h.members {
		if strings.Contains(strings.ToLower(n), name) {
			hit = append(hit, id)
		}
	}
	if len(hit) == 1 {
		return hit[0], nil
	}
	var names []string
	for _, n := range h.members {
		names = append(names, n)
	}
	sort.Strings(names)
	return "", fmt.Errorf("member %q: %d match. Members: %s", name, len(hit), strings.Join(names, ", "))
}

func (s *System) lhProjects(ctx context.Context, tr cal.Transport, h lhHouse) ([]lh.Project, error) {
	lim := uint64(500)
	l, err := lh.NewProjectClient(tr).ListProjects(ctx, lh.HouseScopedListRequest{HouseId: lh.HouseID(h.id), Limit: &lim})
	return l.Projects, err
}

func pickProject(ps []lh.Project, want string) (lh.Project, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	var hit []lh.Project
	for _, p := range ps {
		n := strings.ToLower(p.Name)
		if n == want {
			return p, nil
		}
		if strings.Contains(n, want) || strings.Contains(want, n) {
			hit = append(hit, p)
		}
	}
	if len(hit) == 1 {
		return hit[0], nil
	}
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return lh.Project{}, fmt.Errorf("project %q: %d match. Projects: %s", want, len(hit), strings.Join(names, "; "))
}

func (s *System) describeTask(t lh.Task, h lhHouse, projects map[lh.TaskID][]string) string {
	loc := s.zone()
	var b strings.Builder
	b.WriteString(t.Title)
	st := "open"
	if t.Status != nil {
		st = string(*t.Status)
	}
	b.WriteString(" [" + st + "]")
	if t.DueAt != nil {
		b.WriteString(" due " + parseTS(cal.Timestamp(*t.DueAt)).In(loc).Format("Mon 2006-01-02 15:04"))
	}
	var who []string
	for _, a := range t.Assignees {
		who = append(who, h.members[a])
	}
	if len(who) > 0 {
		b.WriteString(" for " + strings.Join(who, ", "))
	} else if n := h.members[t.OwnerMemberId]; n != "" {
		b.WriteString(" owner " + n)
	}
	if ps := projects[t.TaskId]; len(ps) > 0 {
		b.WriteString(" in " + strings.Join(ps, ", "))
	}
	if t.Tag != nil && *t.Tag != "" {
		b.WriteString(" #" + *t.Tag)
	}
	b.WriteString(" id " + string(t.TaskId))
	b.WriteString(" updated " + parseTS(cal.Timestamp(t.UpdatedAt)).In(loc).Format("2006-01-02"))
	if t.Description != nil && *t.Description != "" {
		b.WriteString("\n  " + clip(strings.ReplaceAll(*t.Description, "\n", " "), 200))
	}
	return b.String()
}

func (s *System) longhouseTool(ctx context.Context, owner, name string, args map[string]string) (string, error) {
	tr := s.Longhouse.transport(owner)
	loc := s.zone()
	h, err := s.lhHouse(ctx, tr, args["house"])
	if err != nil {
		return "", err
	}
	pc, tc := lh.NewProjectClient(tr), lh.NewTaskClient(tr)
	lim := uint64(500)
	switch name {
	case "longhouse_projects":
		ps, err := s.lhProjects(ctx, tr, h)
		if err != nil {
			return "", err
		}
		var lines []string
		for _, p := range ps {
			if p.Status != nil && *p.Status == "archived" && args["archived"] != "true" {
				continue
			}
			if p.Personal != nil && *p.Personal {
				continue
			}
			line := p.Name
			if p.Status != nil && *p.Status != "active" {
				line += " [" + string(*p.Status) + "]"
			}
			if p.Category != nil && *p.Category != "" {
				line += " (" + *p.Category + ")"
			}
			if p.Description != nil && *p.Description != "" {
				line += ": " + clip(*p.Description, 160)
			}
			lines = append(lines, line+" id "+string(p.ProjectId))
		}
		return fmt.Sprintf("House %s, %d projects:\n%s", h.name, len(lines), strings.Join(lines, "\n")), nil
	case "longhouse_tasks":
		ps, err := s.lhProjects(ctx, tr, h)
		if err != nil {
			return "", err
		}
		var state *lh.TaskState
		switch args["state"] {
		case "closed":
			state = ptr(lh.TaskState("closed"))
		case "all":
		default:
			state = ptr(lh.TaskState("open"))
		}
		// Which projects hold each task.
		in := map[lh.TaskID][]string{}
		var tasks []lh.Task
		if want := args["project"]; want != "" {
			p, err := pickProject(ps, want)
			if err != nil {
				return "", err
			}
			l, err := pc.ListProjectTasks(ctx, lh.ProjectScopedListRequest{HouseId: lh.HouseID(h.id), ProjectId: p.ProjectId, Limit: &lim, State: state})
			if err != nil {
				return "", err
			}
			tasks = l.Tasks
			for _, t := range tasks {
				in[t.TaskId] = append(in[t.TaskId], p.Name)
			}
		} else {
			req := lh.TaskListRequest{HouseId: lh.HouseID(h.id), Limit: &lim, State: state}
			if args["mine"] == "true" {
				req.Mine = ptr(true)
			}
			l, err := tc.ListTasks(ctx, req)
			if err != nil {
				return "", err
			}
			tasks = l.Tasks
			for _, p := range ps {
				if p.Personal != nil && *p.Personal {
					continue
				}
				pl, err := pc.ListProjectTasks(ctx, lh.ProjectScopedListRequest{HouseId: lh.HouseID(h.id), ProjectId: p.ProjectId, Limit: &lim})
				if err != nil {
					continue
				}
				for _, t := range pl.Tasks {
					in[t.TaskId] = append(in[t.TaskId], p.Name)
				}
			}
		}
		var lines []string
		for _, t := range tasks {
			if w := args["text"]; w != "" && !anyWord(t.Title+" "+deref(t.Description)+" "+deref(t.Tag), w) {
				continue
			}
			lines = append(lines, s.describeTask(t, h, in))
		}
		if len(lines) == 0 {
			return "House " + h.name + ": no tasks match.", nil
		}
		head := fmt.Sprintf("House %s, %d tasks:", h.name, len(lines))
		if len(lines) > 80 {
			head += " (the first 80; filter by project or words)"
			lines = lines[:80]
		}
		return head + "\n" + strings.Join(lines, "\n"), nil
	case "add_longhouse_task":
		title := strings.TrimSpace(args["title"])
		if title == "" {
			return "", errors.New("give a title")
		}
		who, err := h.memberID(args["assignee"])
		if err != nil {
			return "", err
		}
		t := lh.Task{HouseId: lh.HouseID(h.id), OwnerMemberId: lh.MemberID(h.member), Assignees: []lh.MemberID{who}, Title: clip(title, 500)}
		if v := strings.TrimSpace(args["description"]); v != "" {
			t.Description = &v
		}
		if v := strings.TrimSpace(args["due"]); v != "" {
			d, date, err := localTime(v, loc)
			if err != nil {
				return "", fmt.Errorf("due: %v", err)
			}
			if date {
				d = d.Add(17 * time.Hour)
			}
			t.DueAt = ptr(lh.Timestamp(d.UTC().Format(time.RFC3339)))
		}
		var proj *lh.Project
		if v := args["project"]; v != "" {
			ps, err := s.lhProjects(ctx, tr, h)
			if err != nil {
				return "", err
			}
			p, err := pickProject(ps, v)
			if err != nil {
				return "", err
			}
			proj = &p
		}
		got, err := tc.CreateTask(ctx, t)
		if err != nil {
			return "", err
		}
		in := map[lh.TaskID][]string{}
		if proj != nil {
			if _, err := pc.AddProjectTask(ctx, lh.ProjectTaskOrderRequest{ProjectId: proj.ProjectId, TaskId: got.TaskId, Position: 0}); err != nil {
				return "Created, but not added to " + proj.Name + ": " + err.Error() + "\n" + s.describeTask(got, h, in), nil
			}
			in[got.TaskId] = []string{proj.Name}
		}
		return "Created: " + s.describeTask(got, h, in), nil
	case "update_longhouse_task":
		id := lh.TaskID(strings.TrimSpace(args["task"]))
		t, err := tc.GetTask(ctx, id)
		if err != nil {
			return "", err
		}
		changed := false
		if v := args["status"]; v != "" {
			t.Status, changed = ptr(lh.TaskStatus(v)), true
		}
		if v := strings.TrimSpace(args["title"]); v != "" {
			t.Title, changed = clip(v, 500), true
		}
		if v := strings.TrimSpace(args["description"]); v != "" {
			t.Description, changed = &v, true
		}
		if v := strings.TrimSpace(args["due"]); v != "" {
			d, date, err := localTime(v, loc)
			if err != nil {
				return "", fmt.Errorf("due: %v", err)
			}
			if date {
				d = d.Add(17 * time.Hour)
			}
			t.DueAt, changed = ptr(lh.Timestamp(d.UTC().Format(time.RFC3339))), true
		}
		out := ""
		if changed {
			if t, err = tc.UpdateTask(ctx, t); err != nil {
				return "", err
			}
			out = "Changed: " + s.describeTask(t, h, nil)
		}
		if v := strings.TrimSpace(args["comment"]); v != "" {
			if _, err := lh.NewCommentClient(tr).CreateComment(ctx, lh.Comment{HouseId: t.HouseId, TargetType: "task", TargetId: string(t.TaskId), Body: clip(v, 4000)}); err != nil {
				return out, err
			}
			out = strings.TrimSpace(out + "\nComment added.")
		}
		if out == "" {
			return "", errors.New("give something to change")
		}
		return out, nil
	case "add_longhouse_project":
		p := lh.Project{HouseId: lh.HouseID(h.id), Name: clip(strings.TrimSpace(args["name"]), 200)}
		if p.Name == "" {
			return "", errors.New("give a name")
		}
		if v := strings.TrimSpace(args["description"]); v != "" {
			p.Description = &v
		}
		got, err := pc.CreateProject(ctx, p)
		if err != nil {
			return "", err
		}
		return "Created project " + got.Name + " in " + h.name + " id " + string(got.ProjectId), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}
