package compose

import (
	"context"
	"fmt"
	"strings"
	"time"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// The health of a device (docs/attention.md, section 7.1). A device reports
// its battery and the counters of hands-free talk with each hello. Code
// computes the battery use from the reports: a persona gets the result as
// text, and no model reads the reports.

const (
	// healthWindow is the time that the battery use is computed for.
	healthWindow = 24 * time.Hour
	// reportGap: two reports that are further apart than this have a time
	// between them with no reports (the device was off, or had no
	// network). That time is not counted.
	reportGap = 5 * time.Minute
	// minUseTime is the least time of reports for a battery rate. The
	// battery is a whole number of percent, so a short time gives a rate
	// that means nothing.
	minUseTime = 2 * time.Hour
)

// batteryUse is the fall of the battery of a device while it did not
// charge, for the time with the microphone on and the time with it off.
type batteryUse struct {
	onDrop, offDrop float64       // percent
	onTime, offTime time.Duration // time that the drops are from
	// Counters of all reports of the window.
	listen, sound         time.Duration
	wakes, turns, dropped uint64
}

// useOf computes the battery use from the reports of one device, oldest
// first. It counts the time between two reports when the two are near in
// time, the device did not charge at either, and the battery did not go
// up. The microphone was on in that time when the later report says that
// it listened for half of its window or more.
func useOf(reports []conversationapi.DeviceReport) batteryUse {
	var u batteryUse
	for i, r := range reports {
		h := r.Health
		u.listen += time.Duration(deref(h.ListenS)) * time.Second
		u.sound += time.Duration(deref(h.SoundS)) * time.Second
		u.wakes, u.turns, u.dropped = u.wakes+deref(h.Wakes), u.turns+deref(h.Turns), u.dropped+deref(h.Dropped)
		if i == 0 {
			continue
		}
		p := reports[i-1].Health
		dt := time.Duration(r.AtMs-reports[i-1].AtMs) * time.Millisecond
		if r.AtMs <= reports[i-1].AtMs || dt > reportGap || h.Battery == nil || p.Battery == nil ||
			h.Charging == nil || p.Charging == nil || *h.Charging || *p.Charging || *h.Battery > *p.Battery {
			continue
		}
		drop := float64(*p.Battery - *h.Battery)
		if w := deref(h.WindowS); w > 0 && 2*deref(h.ListenS) >= w {
			u.onDrop, u.onTime = u.onDrop+drop, u.onTime+dt
		} else {
			u.offDrop, u.offTime = u.offDrop+drop, u.offTime+dt
		}
	}
	return u
}

// rate is the text of one battery rate, or how much data is missing.
func rate(what string, drop float64, d time.Duration) string {
	if d < minUseTime {
		return fmt.Sprintf("%s: not enough data (%d minutes of reports; %d minutes are necessary)", what, int(d.Minutes()), int(minUseTime.Minutes()))
	}
	return fmt.Sprintf("%s: %.1f percent for each hour (from %.1f hours of reports)", what, drop/d.Hours(), d.Hours())
}

// describeHealthOf is the health of one device of owner for a persona:
// the last report, and the battery use of the last 24 hours.
func (s *System) describeHealthOf(ctx context.Context, owner string, d conversationapi.Device, now time.Time) string {
	var b strings.Builder
	h := d.Health
	if h.Battery != nil {
		fmt.Fprintf(&b, "  battery %d percent", *h.Battery)
		if h.Charging != nil {
			if *h.Charging {
				b.WriteString(", charging")
			} else {
				b.WriteString(", not charging")
			}
		}
		b.WriteString("\n")
	}
	res, err := s.Conversation.ListDeviceReports(s.AsService(ctx), conversationapi.ListDeviceReportsRequest{Device: d.Id, Person: ptr(owner),
		SinceMs: ptr(uint64(now.Add(-healthWindow).UnixMilli()))})
	if err != nil {
		fmt.Fprintf(&b, "  (the reports of the device are not available: %v)\n", err)
		return b.String()
	}
	if len(res.Reports) < 2 {
		return b.String()
	}
	u := useOf(res.Reports)
	fmt.Fprintf(&b, "  battery use in the last 24 hours while not charging (computed from %d reports): %s; %s\n", len(res.Reports),
		rate("with the microphone on", u.onDrop, u.onTime), rate("with the microphone off", u.offDrop, u.offTime))
	if u.listen > 0 {
		fmt.Fprintf(&b, "  hands-free talk in the last 24 hours: the microphone was on for %d minutes, and the device heard sound for %d minutes of that; %d wake names, %d turns sent, %d turns dropped (not the voice of the person)\n",
			int(u.listen.Minutes()), int(u.sound.Minutes()), u.wakes, u.turns, u.dropped)
	}
	return b.String()
}
