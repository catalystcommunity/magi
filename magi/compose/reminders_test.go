package compose

import (
	"strings"
	"testing"
	"time"

	identityapi "github.com/catalystcommunity/magi/primitives/identity/api"
)

func TestGrantReminder(t *testing.T) {
	end := time.Date(2026, 12, 10, 16, 21, 0, 0, time.UTC)
	n := grantReminder(identityapi.Grant{GrantId: "g", Owner: "user:a@example.local", Audience: identityapi.AppRef{AppId: "catalystcal"},
		RenewableUntilMs: uint64(end.UnixMilli())}, time.UTC)
	if n.owner != "user:a@example.local" || n.title != "Connect your calendar again" || !strings.Contains(n.text, "Thursday 10 December at 16:21") || !strings.Contains(n.text, "connect my calendar") {
		t.Errorf("%+v", n)
	}
}
