package compose

import (
	"context"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

// report is one report of a device: the battery, if it charges, and the
// seconds of its window that the microphone was on.
func report(at time.Time, battery uint64, charging bool, window, listen uint64) conversationapi.DeviceReport {
	return conversationapi.DeviceReport{AtMs: uint64(at.UnixMilli()), Health: conversationapi.DeviceHealth{
		Battery: &battery, Charging: &charging, WindowS: &window, ListenS: &listen}}
}

func TestBatteryUse(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	var rs []conversationapi.DeviceReport
	at, battery := t0, 100.0
	add := func(minutes int, perHour float64, charging bool, listen uint64) {
		for range minutes {
			at = at.Add(time.Minute)
			battery -= perHour / 60
			rs = append(rs, report(at, uint64(math.Round(battery)), charging, 60, listen))
		}
	}
	rs = append(rs, report(at, 100, false, 60, 0))
	add(180, 3, false, 60) // 3 hours with the microphone on: 3 percent for each hour
	add(240, 1, false, 0)  // 4 hours with it off: 1 percent for each hour
	// One hour on the charger: the battery goes up, and the time is not counted.
	for range 60 {
		at = at.Add(time.Minute)
		battery += 0.5
		rs = append(rs, report(at, uint64(math.Round(battery)), true, 60, 60))
	}
	// The phone was off for 2 hours and lost 20 percent: a gap, not counted.
	at, battery = at.Add(2*time.Hour), battery-20
	rs = append(rs, report(at, uint64(math.Round(battery)), false, 60, 0))
	add(30, 2, false, 60)
	u := useOf(rs)
	if got := u.onDrop / u.onTime.Hours(); math.Abs(got-3) > 0.35 || math.Abs(u.onTime.Hours()-3.5) > 0.05 {
		t.Errorf("microphone on: %.2f percent for each hour over %.2f hours, want about 3 over 3.5", got, u.onTime.Hours())
	}
	if got := u.offDrop / u.offTime.Hours(); math.Abs(got-1) > 0.15 || math.Abs(u.offTime.Hours()-4) > 0.05 {
		t.Errorf("microphone off: %.2f percent for each hour over %.2f hours, want about 1 over 4", got, u.offTime.Hours())
	}
	if u.listen != (180+60+30)*time.Minute {
		t.Errorf("listen = %s", u.listen)
	}

	// Reports with no battery, and one report, give no use.
	if u := useOf([]conversationapi.DeviceReport{{AtMs: 1000}, {AtMs: 61000}}); u.onTime+u.offTime != 0 {
		t.Errorf("use with no battery = %+v", u)
	}
	if u := useOf(rs[:1]); u.onTime+u.offTime != 0 {
		t.Errorf("use of one report = %+v", u)
	}
}

// healthConversation has the devices and the reports of one person, and
// it keeps the last put-device.
type healthConversation struct {
	conversationapi.ConversationService
	mu      sync.Mutex
	devices []conversationapi.Device
	reports map[string][]conversationapi.DeviceReport
	asked   []conversationapi.ListDeviceReportsRequest
	put     *conversationapi.PutDeviceRequest
}

func (f *healthConversation) ListDevices(context.Context, conversationapi.ListDevicesRequest) (conversationapi.ListDevicesResponse, error) {
	return conversationapi.ListDevicesResponse{Devices: f.devices}, nil
}

func (f *healthConversation) ListDeviceReports(_ context.Context, r conversationapi.ListDeviceReportsRequest) (conversationapi.ListDeviceReportsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r)
	out := conversationapi.ListDeviceReportsResponse{Reports: []conversationapi.DeviceReport{}}
	for _, rep := range f.reports[r.Device] {
		if rep.AtMs >= deref(r.SinceMs) {
			out.Reports = append(out.Reports, rep)
		}
	}
	return out, nil
}

func (f *healthConversation) PutDevice(_ context.Context, r conversationapi.PutDeviceRequest) (conversationapi.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put = &r
	return conversationapi.Device{Id: r.Id, Kind: r.Kind, Name: r.Name, Can: []string{}, Health: r.Health}, nil
}

func TestDescribeDeviceHealth(t *testing.T) {
	now := time.Now()
	var day, short, old []conversationapi.DeviceReport
	// 6 hours of reports each minute: 3 hours with the microphone on (2
	// percent for each hour), then 3 hours with it off (1 percent).
	for i := 0; i <= 360; i++ {
		at := now.Add(time.Duration(i-360) * time.Minute)
		b, listen := 100-float64(i)*2/60, uint64(60)
		if i > 180 {
			b, listen = 94-float64(i-180)/60, 0
		}
		r := report(at, uint64(math.Round(b)), false, 60, listen)
		if i > 0 && i <= 180 {
			r.Health.SoundS, r.Health.Wakes, r.Health.Turns, r.Health.Dropped = ptr(uint64(6)), ptr(uint64(i%60/59)), ptr(uint64(i%30/29)), ptr(uint64(i%90/89))
		}
		day = append(day, r)
	}
	for i := 0; i <= 30; i++ {
		short = append(short, report(now.Add(time.Duration(i-30)*time.Minute), uint64(80-i/10), false, 60, 0))
	}
	// Reports from 3 days before are not in the window.
	for i := 0; i <= 300; i++ {
		old = append(old, report(now.Add(-72*time.Hour+time.Duration(i)*time.Minute), uint64(90-i/10), false, 60, 60))
	}
	fc := &healthConversation{reports: map[string][]conversationapi.DeviceReport{"phone-1": day, "tablet-1": short, "old-1": old}}
	fc.devices = []conversationapi.Device{
		{Id: "phone-1", Kind: "phone", Name: "Pixel", HandsFree: true, SeenMs: uint64(now.UnixMilli()), Health: &day[len(day)-1].Health},
		{Id: "tablet-1", Kind: "phone", Name: "Tablet", SeenMs: uint64(now.UnixMilli()), Health: &conversationapi.DeviceHealth{Battery: ptr(uint64(77)), Charging: ptr(true)}},
		{Id: "old-1", Kind: "phone", Name: "Old phone", SeenMs: uint64(now.Add(-70 * time.Hour).UnixMilli()), Health: &old[len(old)-1].Health},
		{Id: "web-1", Kind: "browser", Name: "Firefox", SeenMs: uint64(now.UnixMilli())},
	}
	sys := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sys.Conversation = conversationClient(t, fc)
	sys.Cfg.ServiceToken = testToken
	out := sys.describeDevices(context.Background(), "user:kim@example.local")
	for _, want := range []string{
		"- phone-1: Pixel (phone), not in use, hands-free",
		"  battery 91 percent, not charging\n",
		"computed from 361 reports): with the microphone on: 2.0 percent for each hour (from 3.0 hours of reports); with the microphone off: 1.0 percent for each hour (from 3.0 hours of reports)",
		"the microphone was on for 181 minutes, and the device heard sound for 18 minutes of that; 3 wake names, 6 turns sent, 2 turns dropped",
		"  battery 77 percent, charging\n",
		"with the microphone on: not enough data (0 minutes of reports; 120 minutes are necessary); with the microphone off: not enough data (30 minutes of reports; 120 minutes are necessary)",
		"- web-1: Firefox (browser)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the devices text has no %q:\n%s", want, out)
		}
	}
	// A device with no report in the window has its last battery and no use.
	if i := strings.Index(out, "- old-1"); i < 0 || !strings.HasPrefix(out[i:], "- old-1: Old phone (phone), not in use, seen 70h0m0s ago\n  battery 60 percent, not charging\n- web-1") {
		t.Errorf("the old device:\n%s", out)
	}
	// Magi reads the reports for the person, and only of the last 24 hours.
	for _, r := range fc.asked {
		if deref(r.Person) != "user:kim@example.local" || now.Sub(time.UnixMilli(int64(deref(r.SinceMs)))) > 25*time.Hour {
			t.Errorf("reports request = %+v", r)
		}
	}
	if len(fc.asked) != 3 {
		t.Errorf("Magi asked for the reports of %d devices, want 3 (the browser has no health)", len(fc.asked))
	}
}

// put-device of Magi gives the report of the device to the conversation
// service, and the device that comes back has it.
func TestPutDeviceHealth(t *testing.T) {
	fc := &healthConversation{}
	_, client, as := adminEnv(t, Config{}, map[string]map[string]func(context.Context, []byte) ([]byte, error){
		"ConversationService": conversationapi.ConversationServiceOps(fc),
	})
	d, err := client.PutDevice(as("user:kim@example.local"), magiapi.PutDeviceRequest{Id: "phone-0001", Kind: "phone", Name: "Pixel",
		Health: &magiapi.DeviceHealth{Battery: ptr(uint64(64)), Charging: ptr(false), WindowS: ptr(uint64(60)), ListenS: ptr(uint64(58)),
			SoundS: ptr(uint64(4)), Wakes: ptr(uint64(1)), Turns: ptr(uint64(2)), Dropped: ptr(uint64(3))}})
	if err != nil {
		t.Fatal(err)
	}
	h := fc.put.Health
	if h == nil || *h.Battery != 64 || *h.Charging || *h.WindowS != 60 || *h.ListenS != 58 || *h.SoundS != 4 || *h.Wakes != 1 || *h.Turns != 2 || *h.Dropped != 3 {
		t.Fatalf("the conversation service got %+v", h)
	}
	if d.Health == nil || deref(d.Health.Battery) != 64 || deref(d.Health.Dropped) != 3 {
		t.Fatalf("device = %+v", d)
	}
	// A hello with no report sends none.
	if _, err := client.PutDevice(as("user:kim@example.local"), magiapi.PutDeviceRequest{Id: "phone-0001", Kind: "phone", Name: "Pixel"}); err != nil || fc.put.Health != nil {
		t.Fatalf("put with no health sent %+v, %v", fc.put.Health, err)
	}
}
