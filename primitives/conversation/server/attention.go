package server

import (
	"context"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/catalystcommunity/magi/primitives/conversation/store"
)

// Devices and notices (docs/attention.md). A device and a notice belong
// to one person. Only that person, or a privileged caller, can see them.

var (
	deviceKinds = []string{"phone", "browser", "desktop", "speaker"}
	deviceCan   = []string{"speak", "notify", "show"}
	urgencies   = []string{"low", "normal", "high"}
	deliveryHow = []string{"spoken", "shown", "notified"}
	deviceIDRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

const (
	maxDeviceName = 80
	maxTitle      = 120
	// noticeWindow is how long a notice that nobody acknowledged goes to
	// devices.
	noticeWindow = 24 * time.Hour
)

// person returns the person that a call acts for: the caller, or person
// for a privileged caller.
func (s *Service) person(ctx context.Context, person *string) (string, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return "", err
	}
	who := deref(person)
	if who == "" || who == p.Subject {
		return p.Subject, nil
	}
	if !privileged(p) {
		return "", rpc.Denied("acting for %s needs %s", who, ScopeActAs)
	}
	return who, nil
}

func (s *Service) PutDevice(ctx context.Context, req api.PutDeviceRequest) (api.Device, error) {
	who, err := s.person(ctx, nil)
	if err != nil {
		return api.Device{}, err
	}
	switch {
	case !deviceIDRE.MatchString(req.Id):
		return api.Device{}, rpc.Invalid("device id must be 8 to 64 letters, digits, '_', or '-'")
	case !slices.Contains(deviceKinds, req.Kind):
		return api.Device{}, rpc.Invalid("kind must be one of %v", deviceKinds)
	case req.Name == "" || utf8.RuneCountInString(req.Name) > maxDeviceName:
		return api.Device{}, rpc.Invalid("name must be 1 to %d characters", maxDeviceName)
	}
	for _, c := range req.Can {
		if !slices.Contains(deviceCan, c) {
			return api.Device{}, rpc.Invalid("can must be from %v", deviceCan)
		}
	}
	now := s.nowMs()
	return s.DB.PutDevice(ctx, api.Device{Person: who, Id: req.Id, Kind: req.Kind, Name: req.Name, Can: req.Can,
		Active: deref(req.Active), HandsFree: deref(req.HandsFree), SeenMs: now})
}

func (s *Service) ListDevices(ctx context.Context, req api.ListDevicesRequest) (api.ListDevicesResponse, error) {
	who, err := s.person(ctx, req.Person)
	if err != nil {
		return api.ListDevicesResponse{}, err
	}
	list, err := s.DB.ListDevices(ctx, who)
	return api.ListDevicesResponse{Devices: list}, err
}

func (s *Service) RemoveDevice(ctx context.Context, req api.RemoveDeviceRequest) (api.Device, error) {
	who, err := s.person(ctx, nil)
	if err != nil {
		return api.Device{}, err
	}
	d, err := s.DB.RemoveDevice(ctx, who, req.Id)
	return d, notFound(err, "device %s", req.Id)
}

func (s *Service) Notify(ctx context.Context, req api.NotifyRequest) (api.Notice, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Notice{}, err
	}
	if !privileged(p) {
		return api.Notice{}, rpc.Denied("notify needs %s", ScopeActAs)
	}
	urgency := req.Urgency
	if deref(urgency) == "" {
		urgency = ptr("normal")
	}
	switch {
	case !slices.Contains(urgencies, *urgency):
		return api.Notice{}, rpc.Invalid("urgency must be one of %v", urgencies)
	case req.Title == "" || utf8.RuneCountInString(req.Title) > maxTitle:
		return api.Notice{}, rpc.Invalid("title must be 1 to %d characters", maxTitle)
	}
	u, err := s.DB.GetUtterance(ctx, store.All, req.Utterance)
	if err != nil {
		return api.Notice{}, notFound(err, "utterance %s", req.Utterance)
	}
	st, err := s.DB.GetStream(ctx, u.Stream)
	if err != nil {
		return api.Notice{}, notFound(err, "stream %s", u.Stream)
	}
	if dev := deref(req.Device); dev != "" {
		if _, err := s.DB.GetDevice(ctx, st.Person, dev); err != nil {
			return api.Notice{}, notFound(err, "device %s of %s", dev, st.Person)
		}
	} else {
		req.Device = nil
	}
	n := api.Notice{Id: newID("ntc_"), Person: st.Person, Persona: st.Persona, Stream: st.Id, Utterance: u.Id,
		Title: req.Title, Text: u.Text, Urgency: *urgency, Device: req.Device, CreatedMs: s.nowMs()}
	return s.DB.PutNotice(ctx, n)
}

func (s *Service) WaitNotices(ctx context.Context, req api.WaitNoticesRequest) (api.WaitNoticesResponse, error) {
	who, err := s.person(ctx, nil)
	if err != nil {
		return api.WaitNoticesResponse{}, err
	}
	if _, err := s.DB.GetDevice(ctx, who, req.Device); err != nil {
		return api.WaitNoticesResponse{}, notFound(err, "device %s (say hello with put-device first)", req.Device)
	}
	var wait time.Duration
	if req.WaitMs != nil {
		wait = min(time.Duration(*req.WaitMs)*time.Millisecond, MaxWait)
	}
	poll := s.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		ch := s.waitChan(store.NoticeKey(who))
		now := s.nowMs()
		if err := s.DB.TouchDevice(ctx, who, req.Device, now); err != nil {
			return api.WaitNoticesResponse{}, err
		}
		pending, err := s.DB.PendingNotices(ctx, who, req.Device, now-uint64(noticeWindow.Milliseconds()))
		if err != nil {
			return api.WaitNoticesResponse{}, err
		}
		closed, err := s.DB.ClosedNotices(ctx, who, req.Device, deref(req.ClosedAfter))
		if err != nil {
			return api.WaitNoticesResponse{}, err
		}
		left := time.Until(deadline)
		if len(pending) > 0 || len(closed) > 0 || left <= 0 {
			return api.WaitNoticesResponse{Notices: pending, Closed: closed, NowMs: now}, nil
		}
		t := time.NewTimer(min(left, poll))
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return api.WaitNoticesResponse{}, ctx.Err()
		}
		t.Stop()
	}
}

// ownNotice returns notice id when it belongs to the caller (or the caller
// is privileged).
func (s *Service) ownNotice(ctx context.Context, id string) (api.Notice, string, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Notice{}, "", err
	}
	n, err := s.DB.GetNotice(ctx, id)
	if err != nil || (n.Person != p.Subject && !privileged(p)) {
		return api.Notice{}, "", rpc.NotFound("notice %s", id)
	}
	return n, p.Subject, nil
}

func (s *Service) DeliverNotice(ctx context.Context, req api.DeliverNoticeRequest) (api.Notice, error) {
	n, _, err := s.ownNotice(ctx, req.Id)
	if err != nil {
		return n, err
	}
	if !slices.Contains(deliveryHow, req.How) {
		return n, rpc.Invalid("how must be one of %v", deliveryHow)
	}
	if _, err := s.DB.GetDevice(ctx, n.Person, req.Device); err != nil {
		return n, notFound(err, "device %s", req.Device)
	}
	if err := s.DB.DeliverNotice(ctx, n.Id, req.Device, req.How, s.nowMs()); err != nil {
		return n, err
	}
	return s.DB.GetNotice(ctx, n.Id)
}

func (s *Service) AckNotice(ctx context.Context, req api.AckNoticeRequest) (api.Notice, error) {
	n, caller, err := s.ownNotice(ctx, req.Id)
	if err != nil {
		return n, err
	}
	by := caller
	if dev := deref(req.Device); dev != "" {
		if _, err := s.DB.GetDevice(ctx, n.Person, dev); err != nil {
			return n, notFound(err, "device %s", dev)
		}
		by = "device:" + dev
	}
	if n.AckedMs == nil {
		if err := s.DB.AckNotice(ctx, n, by, s.nowMs()); err != nil {
			return n, err
		}
	}
	return s.DB.GetNotice(ctx, n.Id)
}

func (s *Service) ListNotices(ctx context.Context, req api.ListNoticesRequest) (api.ListNoticesResponse, error) {
	who, err := s.person(ctx, req.Person)
	if err != nil {
		return api.ListNoticesResponse{}, err
	}
	list, err := s.DB.ListNotices(ctx, who, deref(req.Open), limit(req.Limit))
	return api.ListNoticesResponse{Notices: list}, err
}
