// Package renew keeps the token file of a machine (inference:<name> or
// runner:<name>) valid. It calls IdentityService/refresh with the current
// token and replaces the file before the token expires.
package renew

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
)

// MaxLife: a token that lasts longer than this renews at once, so a minted
// long token becomes a short renewed one. It matches server.MachineTokenTTL
// plus one day.
const MaxLife = 31 * 24 * time.Hour

// Renewer replaces File with a new token when 2/3 of the life of the
// current token has passed.
type Renewer struct {
	File string // token file; written as a new file, then renamed
	URL  string // Magi origin, such as https://magi.example.local
	HTTP *http.Client
	Log  *slog.Logger
	Now  func() time.Time
}

// times reads the issue and expiry times of a token without a signature
// check. The server checks the signature when the token is used.
func times(tok string) (iat, exp time.Time, err error) {
	const prefix = "magi1."
	body, _, ok := strings.Cut(strings.TrimPrefix(tok, prefix), ".")
	if !ok || !strings.HasPrefix(tok, prefix) {
		return iat, exp, errors.New("not a Magi token")
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return iat, exp, err
	}
	var p struct{ Iat, Exp int64 }
	if err := json.Unmarshal(b, &p); err != nil || p.Exp == 0 {
		return iat, exp, errors.New("token has no expiry")
	}
	return time.Unix(p.Iat, 0), time.Unix(p.Exp, 0), nil
}

func (r *Renewer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Renewer) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Due returns when the token in File must renew.
func (r *Renewer) Due() (time.Time, error) {
	b, err := os.ReadFile(r.File)
	if err != nil {
		return time.Time{}, err
	}
	iat, exp, err := times(strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}, err
	}
	if exp.Sub(iat) > MaxLife {
		return r.now(), nil
	}
	return iat.Add(exp.Sub(iat) * 2 / 3), nil
}

// Renew replaces File with a new token now.
func (r *Renewer) Renew(ctx context.Context) error {
	b, err := os.ReadFile(r.File)
	if err != nil {
		return err
	}
	c := rpc.NewClient(r.URL, rpc.StaticToken(strings.TrimSpace(string(b))))
	if r.HTTP != nil {
		c.HTTP = r.HTTP
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	res, err := api.NewIdentityClient(c).Refresh(ctx, api.RefreshRequest{})
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	tmp := filepath.Join(filepath.Dir(r.File), "."+filepath.Base(r.File)+".new")
	if err := os.WriteFile(tmp, []byte(res.Token+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.File); err != nil {
		os.Remove(tmp)
		return err
	}
	r.log().Info("token renewed", "subject", res.Subject, "expires", time.UnixMilli(int64(res.ExpiresMs)).UTC().Format(time.RFC3339))
	return nil
}

// Run renews the token when it is due, until ctx ends. After a failure it
// tries again in 1 minute, then up to every hour. A token that expired
// cannot renew; then a person must mint a new one.
func (r *Renewer) Run(ctx context.Context) {
	retry := time.Minute
	for {
		wait := time.Hour
		due, err := r.Due()
		if err != nil {
			r.log().Error("token renewal", "err", err)
		} else if d := due.Sub(r.now()); d > 0 {
			wait = min(d, 12*time.Hour)
			retry = time.Minute
		} else if err := r.Renew(ctx); err != nil {
			r.log().Error("token renewal", "err", err, "retry_in", retry.String())
			wait, retry = retry, min(retry*4, time.Hour)
		} else {
			retry = time.Minute
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
