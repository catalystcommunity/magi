package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/conversation/api"
	"github.com/catalystcommunity/magi/primitives/conversation/server"
)

func TestDevicesAndNotices(t *testing.T) {
	// A long poll interval shows that a notice wakes a waiting device
	// through a notification, also from the other replica.
	e := setup(t, 2, 20*time.Second)
	ctx := context.Background()
	must[api.Persona](t)(e.client(0, "user:root@example.local", server.ScopeAdmin).PutPersona(ctx, api.PutPersonaRequest{Id: magi, Name: "Magi", Sharing: "private"}))
	a, b, svc := e.client(0, alice), e.client(0, bob), e.client(1, "service:magi", server.ScopeActAs)
	st := must[api.Stream](t)(a.OpenStream(ctx, api.OpenStreamRequest{Persona: magi}))
	msg := must[api.Utterance](t)(svc.Post(ctx, api.PostRequest{Stream: st.Id, Text: "The report is ready.", Author: ptr(magi), AuthorKind: ptr("persona")}))

	phone := must[api.Device](t)(a.PutDevice(ctx, api.PutDeviceRequest{Id: "phone-0001", Kind: "phone", Name: "Pixel", Can: []string{"speak", "notify"}}))
	if phone.Person != alice || phone.Active || len(phone.Can) != 2 {
		t.Fatalf("device = %+v", phone)
	}
	must[api.Device](t)(a.PutDevice(ctx, api.PutDeviceRequest{Id: "web-00001", Kind: "browser", Name: "Firefox", Active: ptr(true), HandsFree: ptr(true)}))
	// The same id for another person is another device.
	must[api.Device](t)(b.PutDevice(ctx, api.PutDeviceRequest{Id: "phone-0001", Kind: "phone", Name: "Bob's phone"}))
	_, err := a.PutDevice(ctx, api.PutDeviceRequest{Id: "x", Kind: "phone", Name: "P"})
	wantCode(t, err, http.StatusBadRequest)
	_, err = a.PutDevice(ctx, api.PutDeviceRequest{Id: "phone-0002", Kind: "toaster", Name: "P"})
	wantCode(t, err, http.StatusBadRequest)
	if l := must[api.ListDevicesResponse](t)(a.ListDevices(ctx, api.ListDevicesRequest{})); len(l.Devices) != 2 {
		t.Fatalf("devices = %+v", l)
	}
	_, err = a.ListDevices(ctx, api.ListDevicesRequest{Person: ptr(bob)})
	wantCode(t, err, http.StatusForbidden)

	t.Run("notify needs act-as", func(t *testing.T) {
		_, err := a.Notify(ctx, api.NotifyRequest{Utterance: msg.Id, Title: "Report"})
		wantCode(t, err, http.StatusForbidden)
		_, err = svc.Notify(ctx, api.NotifyRequest{Utterance: msg.Id, Title: "Report", Device: ptr("phone-9999")})
		wantCode(t, err, http.StatusNotFound)
		_, err = svc.Notify(ctx, api.NotifyRequest{Utterance: msg.Id, Title: "Report", Urgency: ptr("panic")})
		wantCode(t, err, http.StatusBadRequest)
	})

	var toPhone api.Notice
	t.Run("a notice wakes the device it is for", func(t *testing.T) {
		got := make(chan api.WaitNoticesResponse, 1)
		go func() {
			r, err := a.WaitNotices(ctx, api.WaitNoticesRequest{Device: "phone-0001", WaitMs: ptr(uint64(15000))})
			if err != nil {
				t.Error(err)
			}
			got <- r
		}()
		time.Sleep(300 * time.Millisecond)
		toPhone = must[api.Notice](t)(svc.Notify(ctx, api.NotifyRequest{Utterance: msg.Id, Title: "Read the report", Device: ptr("phone-0001")}))
		select {
		case r := <-got:
			if len(r.Notices) != 1 || r.Notices[0].Id != toPhone.Id || r.Notices[0].Text != "The report is ready." || r.Notices[0].Urgency != "normal" {
				t.Fatalf("woken = %+v", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("wait-notices did not wake")
		}
		// Not for the browser, and not for the other person.
		if r := must[api.WaitNoticesResponse](t)(a.WaitNotices(ctx, api.WaitNoticesRequest{Device: "web-00001"})); len(r.Notices) != 0 {
			t.Fatalf("browser got %+v", r)
		}
		if r := must[api.WaitNoticesResponse](t)(b.WaitNotices(ctx, api.WaitNoticesRequest{Device: "phone-0001"})); len(r.Notices) != 0 {
			t.Fatalf("bob got %+v", r)
		}
		_, err := b.AckNotice(ctx, api.AckNoticeRequest{Id: toPhone.Id})
		wantCode(t, err, http.StatusNotFound)
	})

	t.Run("a delivered notice does not come again", func(t *testing.T) {
		n := must[api.Notice](t)(a.DeliverNotice(ctx, api.DeliverNoticeRequest{Id: toPhone.Id, Device: "phone-0001", How: "notified"}))
		if len(n.Deliveries) != 1 || n.Deliveries[0].How != "notified" {
			t.Fatalf("delivered = %+v", n)
		}
		if r := must[api.WaitNoticesResponse](t)(a.WaitNotices(ctx, api.WaitNoticesRequest{Device: "phone-0001"})); len(r.Notices) != 0 {
			t.Fatalf("again = %+v", r)
		}
	})

	t.Run("a notice for all devices closes on all", func(t *testing.T) {
		all := must[api.Notice](t)(svc.Notify(ctx, api.NotifyRequest{Utterance: msg.Id, Title: "Task done", Urgency: ptr("high")}))
		for _, d := range []string{"phone-0001", "web-00001"} {
			r := must[api.WaitNoticesResponse](t)(a.WaitNotices(ctx, api.WaitNoticesRequest{Device: d}))
			if len(r.Notices) != 1 || r.Notices[0].Id != all.Id {
				t.Fatalf("%s got %+v", d, r)
			}
			must[api.Notice](t)(a.DeliverNotice(ctx, api.DeliverNoticeRequest{Id: all.Id, Device: d, How: "spoken"}))
		}
		r0 := must[api.WaitNoticesResponse](t)(a.WaitNotices(ctx, api.WaitNoticesRequest{Device: "phone-0001", ClosedAfter: ptr(uint64(0))}))
		// The phone waits; the person acknowledges on the browser.
		got := make(chan api.WaitNoticesResponse, 1)
		go func() {
			r, _ := a.WaitNotices(ctx, api.WaitNoticesRequest{Device: "phone-0001", WaitMs: ptr(uint64(15000)), ClosedAfter: ptr(r0.NowMs)})
			got <- r
		}()
		time.Sleep(300 * time.Millisecond)
		acked := must[api.Notice](t)(e.client(1, alice).AckNotice(ctx, api.AckNoticeRequest{Id: all.Id, Device: ptr("web-00001")}))
		if acked.AckedMs == nil || deref(acked.AckedBy) != "device:web-00001" || len(acked.Deliveries) != 2 {
			t.Fatalf("acked = %+v", acked)
		}
		select {
		case r := <-got:
			if len(r.Closed) != 1 || r.Closed[0] != all.Id {
				t.Fatalf("closed = %+v", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ack did not wake the phone")
		}
		open := must[api.ListNoticesResponse](t)(a.ListNotices(ctx, api.ListNoticesRequest{Open: ptr(true)}))
		if len(open.Notices) != 1 || open.Notices[0].Id != toPhone.Id {
			t.Fatalf("open = %+v", open)
		}
	})

	t.Run("remove a device", func(t *testing.T) {
		must[api.Device](t)(a.RemoveDevice(ctx, api.RemoveDeviceRequest{Id: "web-00001"}))
		_, err := a.WaitNotices(ctx, api.WaitNoticesRequest{Device: "web-00001"})
		wantCode(t, err, http.StatusNotFound)
	})
}

func deref[T any](p *T) T {
	var z T
	if p != nil {
		return *p
	}
	return z
}
