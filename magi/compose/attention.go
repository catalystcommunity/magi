package compose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// Devices and notices (docs/attention.md). The conversation service keeps
// them; Magi passes the calls of a person through with the token of that
// person, and makes notices for personas and tasks.

// maxNoticeWait caps wait_ms of wait-notices, below the timeouts of
// proxies between a client and Magi.
const maxNoticeWait = 25 * time.Second

func device(d conversationapi.Device) magiapi.Device {
	return magiapi.Device{Id: d.Id, Kind: d.Kind, Name: d.Name, Can: d.Can, Active: d.Active, HandsFree: d.HandsFree, SeenMs: d.SeenMs}
}

func notice(n conversationapi.Notice) magiapi.Notice {
	out := magiapi.Notice{Id: n.Id, Persona: n.Persona, Message: n.Utterance, Title: n.Title, Text: n.Text, Urgency: n.Urgency,
		Device: n.Device, CreatedMs: n.CreatedMs, AckedMs: n.AckedMs, Deliveries: []magiapi.NoticeDelivery{}}
	for _, d := range n.Deliveries {
		out.Deliveries = append(out.Deliveries, magiapi.NoticeDelivery{Device: d.Device, How: d.How, AtMs: d.AtMs})
	}
	return out
}

func notices(ns []conversationapi.Notice) []magiapi.Notice {
	out := make([]magiapi.Notice, len(ns))
	for i, n := range ns {
		out[i] = notice(n)
	}
	return out
}

func (m *Service) PutDevice(ctx context.Context, req magiapi.PutDeviceRequest) (magiapi.Device, error) {
	d, err := m.S.Conversation.PutDevice(AsCaller(ctx), conversationapi.PutDeviceRequest{Id: req.Id, Kind: req.Kind, Name: req.Name,
		Can: req.Can, Active: req.Active, HandsFree: req.HandsFree})
	return device(d), err
}

func (m *Service) ListDevices(ctx context.Context, req magiapi.ListDevicesRequest) (magiapi.ListDevicesResponse, error) {
	res, err := m.S.Conversation.ListDevices(AsCaller(ctx), conversationapi.ListDevicesRequest{})
	out := magiapi.ListDevicesResponse{Devices: []magiapi.Device{}}
	for _, d := range res.Devices {
		out.Devices = append(out.Devices, device(d))
	}
	return out, err
}

func (m *Service) RemoveDevice(ctx context.Context, req magiapi.RemoveDeviceRequest) (magiapi.Device, error) {
	d, err := m.S.Conversation.RemoveDevice(AsCaller(ctx), conversationapi.RemoveDeviceRequest{Id: req.Id})
	return device(d), err
}

func (m *Service) WaitNotices(ctx context.Context, req magiapi.WaitNoticesRequest) (magiapi.WaitNoticesResponse, error) {
	if req.WaitMs != nil {
		req.WaitMs = ptr(min(*req.WaitMs, uint64(maxNoticeWait.Milliseconds())))
	}
	res, err := m.S.Conversation.WaitNotices(AsCaller(ctx), conversationapi.WaitNoticesRequest{Device: req.Device, WaitMs: req.WaitMs, ClosedAfter: req.ClosedAfter})
	if res.Closed == nil {
		res.Closed = []string{}
	}
	return magiapi.WaitNoticesResponse{Notices: notices(res.Notices), Closed: res.Closed, NowMs: res.NowMs}, err
}

func (m *Service) DeliverNotice(ctx context.Context, req magiapi.DeliverNoticeRequest) (magiapi.Notice, error) {
	n, err := m.S.Conversation.DeliverNotice(AsCaller(ctx), conversationapi.DeliverNoticeRequest{Id: req.Id, Device: req.Device, How: req.How})
	return notice(n), err
}

func (m *Service) AckNotice(ctx context.Context, req magiapi.AckNoticeRequest) (magiapi.Notice, error) {
	n, err := m.S.Conversation.AckNotice(AsCaller(ctx), conversationapi.AckNoticeRequest{Id: req.Id, Device: req.Device})
	return notice(n), err
}

func (m *Service) ListNotices(ctx context.Context, req magiapi.ListNoticesRequest) (magiapi.ListNoticesResponse, error) {
	res, err := m.S.Conversation.ListNotices(AsCaller(ctx), conversationapi.ListNoticesRequest{Open: req.Open, Limit: req.Limit})
	return magiapi.ListNoticesResponse{Notices: notices(res.Notices)}, err
}

// noticeReq is a notice that Magi sends for a persona or a task.
type noticeReq struct {
	owner, persona string
	stream         string // optional: the stream of owner and persona
	title, text    string
	urgency        string // low, normal, high
	device         string // optional: a device id
	refs           []conversationapi.Ref
	voice          string
}

// sendNotice posts the text as a notice utterance of the persona and
// sends the notice. A notice that is said (normal or high) gets its audio
// in parts, as a spoken reply, so that a client can say it at once.
func (s *System) sendNotice(ctx context.Context, r noticeReq) (conversationapi.Notice, error) {
	sctx := s.AsService(ctx)
	if r.persona == "" {
		r.persona = Persona
	}
	if r.stream == "" {
		st, err := s.Conversation.OpenStream(sctx, conversationapi.OpenStreamRequest{Persona: r.persona, Person: ptr(r.owner)})
		if err != nil {
			return conversationapi.Notice{}, fmt.Errorf("stream: %w", err)
		}
		r.stream = st.Id
	}
	post := conversationapi.PostRequest{Stream: r.stream, Text: r.text, Author: ptr(r.persona), AuthorKind: ptr("persona"),
		Modality: ptr("notice"), Attachments: r.refs}
	for _, ref := range r.refs {
		kind := "refers-to"
		if ref.Kind == "workflow-run" {
			kind = "reports-on"
		}
		post.Links = append(post.Links, conversationapi.NewLink{Target: ref, Kind: kind})
	}
	u, err := s.Conversation.Post(sctx, post)
	if err != nil {
		return conversationapi.Notice{}, fmt.Errorf("post: %w", err)
	}
	n, err := s.Conversation.Notify(sctx, conversationapi.NotifyRequest{Utterance: u.Id, Title: r.title, Urgency: optional(r.urgency), Device: optional(r.device)})
	if err != nil {
		return n, fmt.Errorf("notify: %w", err)
	}
	if r.urgency != "low" {
		go func() {
			bg, cancel := context.WithTimeout(context.WithoutCancel(sctx), 2*time.Minute)
			defer cancel()
			if err := s.speakParts(bg, r.owner, u, r.voice); err != nil {
				s.Log.Warn("notice speech", "utterance", u.Id, "err", err)
			}
		}()
	}
	return n, nil
}

// findDevice finds the device of owner that a persona names: an id, a
// kind ("phone"), or words of its name. "" and "all" mean all devices.
// With more than one match it takes the device that was seen last.
func (s *System) findDevice(ctx context.Context, owner, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(strings.TrimPrefix(name, "my "), "the ")
	if name == "" || name == "all" || name == "everywhere" || name == "any" {
		return "", nil
	}
	res, err := s.Conversation.ListDevices(s.AsService(ctx), conversationapi.ListDevicesRequest{Person: ptr(owner)})
	if err != nil {
		return "", err
	}
	if len(res.Devices) == 0 {
		return "", rpc.NotFound("the person has no devices that use Magi")
	}
	kind := map[string]string{"phone": "phone", "mobile": "phone", "cell": "phone", "browser": "browser", "web": "browser",
		"computer": "desktop", "desktop": "desktop", "laptop": "desktop", "speaker": "speaker"}
	// The list is newest first.
	for _, d := range res.Devices {
		if strings.ToLower(d.Id) == name || strings.ToLower(d.Name) == name {
			return d.Id, nil
		}
	}
	for _, d := range res.Devices {
		if k, ok := kind[name]; ok && d.Kind == k {
			return d.Id, nil
		}
	}
	for _, d := range res.Devices {
		if strings.Contains(strings.ToLower(d.Name), name) {
			return d.Id, nil
		}
	}
	var have []string
	for _, d := range res.Devices {
		have = append(have, fmt.Sprintf("%s (%s)", d.Name, d.Kind))
	}
	return "", rpc.NotFound("no device %q; the devices are: %s", name, strings.Join(have, ", "))
}

// describeDevices is the list of devices of owner for a persona.
func (s *System) describeDevices(ctx context.Context, owner string) string {
	res, err := s.Conversation.ListDevices(s.AsService(ctx), conversationapi.ListDevicesRequest{Person: ptr(owner)})
	if err != nil {
		return "error: " + err.Error()
	}
	if len(res.Devices) == 0 {
		return "No devices."
	}
	var b strings.Builder
	now := time.Now()
	for _, d := range res.Devices {
		state := "not in use"
		if d.Active {
			state = "in use now"
		}
		if d.HandsFree {
			state += ", hands-free"
		}
		fmt.Fprintf(&b, "- %s: %s (%s), %s, seen %s ago\n", d.Id, d.Name, d.Kind, state, now.Sub(time.UnixMilli(int64(d.SeenMs))).Round(time.Second))
	}
	return b.String()
}
