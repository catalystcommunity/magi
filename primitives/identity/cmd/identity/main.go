// Command identity serves IdentityService and the browser login routes.
//
//	identity [serve]                    run the service
//	identity keygen --issuer magi --out key.json [--public key.pub.json]
//	identity mint --key key.json --subject service:x --scopes a,b --ttl 720h --out token.txt
//	identity mint --key key.json --role runner|fleet|inference --name x --out token.txt
//	identity revoke --deny-list deny.json --subject runner:x | --token-id ID   refuse tokens now (services reload the file)
//	identity show --token token.txt   print subject, id and expiry of a token
//	identity stop-machine --subject inference:x [--deny-list deny.json]  refuse renewal; with a deny list, refuse its current tokens too
//	identity app-keys generate --file keys.json --domain d --user-id u --app-id magi --instance-id i
//	identity app-keys enroll --file keys.json --linkkeys-runtime rt.json   (uses the runtime API key)
//	identity app-keys renew --file keys.json --linkkeys-runtime rt.json
//	identity credential-key --out key.txt   make a credential encryption key
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	rp "github.com/catalystcommunity/linkkeys/sdks/regular-rp/go"
	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/identity/linkkeys"
	"github.com/catalystcommunity/magi/primitives/identity/server"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "keygen":
		err = keygen(args)
	case "mint":
		err = mint(args)
	case "revoke":
		err = revoke(args)
	case "show":
		err = show(args)
	case "stop-machine":
		err = stopMachine(args)
	case "app-keys":
		err = appKeys(args)
	case "credential-key":
		err = credentialKey(args)
	default:
		err = fmt.Errorf("unknown command %q (serve, keygen, mint, revoke, show, stop-machine, app-keys, credential-key)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "identity:", err)
		os.Exit(1)
	}
}

func writePrivate(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	issuer := fs.String("issuer", "magi", "issuer name")
	out := fs.String("out", "", "private key file to create (mode 600)")
	pub := fs.String("public", "", "optional public key file to create")
	fs.Parse(args)
	if *out == "" {
		return errors.New("--out is required")
	}
	kf, err := authn.GenerateKeyFile(*issuer)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(kf, "", "  ")
	if err := writePrivate(*out, append(b, '\n')); err != nil {
		return err
	}
	if *pub != "" {
		kf.PrivateKey = ""
		b, _ = json.MarshalIndent(kf, "", "  ")
		if err := os.WriteFile(*pub, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("issuer %s key %s written to %s\n", kf.Issuer, kf.KeyID, *out)
	return nil
}

// Token lifetime limits for `identity mint`.
const (
	// MaxMintTTL is the longest token that mint makes (the admin
	// IssueServiceToken call has the same limit).
	MaxMintTTL = 365 * 24 * time.Hour
	// MaxOperatorTTL is the longest token with operator power: scope
	// magi:operate or a wildcard. It cannot be renewed or stopped, so it
	// is kept short. Revoke one early with `identity revoke`.
	MaxOperatorTTL = 90 * 24 * time.Hour
	// DefaultTTL is the lifetime when --ttl is not given.
	DefaultTTL = 720 * time.Hour
	// DefaultFleetTTL is the default for a fleet (operator) token.
	DefaultFleetTTL = 7 * 24 * time.Hour
)

// operatorScope is the scope for fleet calls (see magi/compose ScopeOperate).
const operatorScope = "magi:operate"

// checkMint refuses a token that would live too long or hold a wildcard
// scope by accident.
func checkMint(subject string, scopes []string, ttl time.Duration, allowWildcard bool) error {
	if ttl <= 0 {
		return errors.New("--ttl must be positive")
	}
	if ttl > MaxMintTTL {
		return fmt.Errorf("--ttl %s is over the limit of %s", ttl, MaxMintTTL)
	}
	operator := false
	for _, s := range scopes {
		if s == "*" {
			if !allowWildcard {
				return errors.New("scope * holds every scope; name the scopes, or pass --allow-wildcard")
			}
			operator = true
		}
		if s == operatorScope {
			operator = true
		}
	}
	if operator && ttl > MaxOperatorTTL {
		return fmt.Errorf("--ttl %s is over the limit of %s for a token with operator scope", ttl, MaxOperatorTTL)
	}
	return nil
}

// roleToken fills subject, scopes and default ttl from --role and --name.
//
//	runner: runner:<name>, scope magi:runner (renews itself)
//	fleet: service:fleet-<name>, scope magi:operate (does not renew)
//	inference: inference:<name>, scope inference:host (renews itself)
func roleToken(role, name string) (subject string, scopes []string, ttl time.Duration, err error) {
	if name == "" || strings.ContainsAny(name, "/:@ \t\n") {
		return "", nil, 0, errors.New("--name is required with --role and must not contain / : @ or spaces")
	}
	switch role {
	case "runner":
		return "runner:" + name, []string{"magi:runner"}, DefaultTTL, nil
	case "fleet":
		return "service:fleet-" + name, []string{operatorScope}, DefaultFleetTTL, nil
	case "inference":
		return "inference:" + name, []string{"inference:host"}, DefaultTTL, nil
	}
	return "", nil, 0, fmt.Errorf("unknown --role %q (runner, fleet, inference)", role)
}

func mint(args []string) error {
	fs := flag.NewFlagSet("mint", flag.ExitOnError)
	key := fs.String("key", env("MAGI_ISSUER_KEY", ""), "issuer key file")
	subject := fs.String("subject", "", "service:<name>, runner:<name>, inference:<name>, or user:<name>@<domain>")
	role := fs.String("role", "", "runner, fleet or inference: sets the subject, scopes and default ttl from --name")
	name := fs.String("name", "", "machine name with --role; otherwise the display name of a user (not an identifier)")
	scopes := fs.String("scopes", "", "comma-separated scopes")
	ttl := fs.Duration("ttl", 0, fmt.Sprintf("token lifetime (default %s, fleet %s; at most %s, operator scope %s)", DefaultTTL, DefaultFleetTTL, MaxMintTTL, MaxOperatorTTL))
	out := fs.String("out", "", "token file to create (mode 600)")
	wildcard := fs.Bool("allow-wildcard", false, "allow the scope *")
	fs.Parse(args)
	var sc []string
	for _, s := range strings.Split(*scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			sc = append(sc, s)
		}
	}
	life := *ttl
	displayName := *name
	if *role != "" {
		displayName = ""
		if *subject != "" {
			return errors.New("--role and --subject are exclusive")
		}
		var rs []string
		var rt time.Duration
		var err error
		if *subject, rs, rt, err = roleToken(*role, *name); err != nil {
			return err
		}
		sc = append(sc, rs...)
		if life == 0 {
			life = rt
		}
	}
	if life == 0 {
		life = DefaultTTL
	}
	if *key == "" || *subject == "" || *out == "" {
		return errors.New("--key, --subject (or --role), and --out are required")
	}
	kind := authn.KindService
	if strings.HasPrefix(*subject, "user:") {
		kind = authn.KindUser
	} else if !strings.HasPrefix(*subject, "service:") && !strings.HasPrefix(*subject, "runner:") && !strings.HasPrefix(*subject, "inference:") {
		// A runner or inference host token is a service token for one
		// machine (see the RunnerService in contracts/magi.csil and
		// primitives/inference, "Connect out").
		return errors.New("subject must start with service:, runner:, inference:, or user:")
	}
	if err := checkMint(*subject, sc, life, *wildcard); err != nil {
		return err
	}
	iss, err := authn.LoadIssuer(*key)
	if err != nil {
		return err
	}
	tok, err := iss.Issue(authn.Principal{Subject: *subject, Kind: kind, Scopes: sc, Name: displayName}, life)
	if err != nil {
		return err
	}
	if err := writePrivate(*out, []byte(tok+"\n")); err != nil {
		return err
	}
	fmt.Printf("token for %s written to %s (expires %s)\n", *subject, *out, time.Now().Add(life).UTC().Format(time.RFC3339))
	return nil
}

// revoke adds a subject or one token id to the deny list file. Services that
// read the file (MAGI_DENY_LIST) refuse the token within authn.DefaultDenyReload
// after the file reaches them.
func revoke(args []string) error {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	file := fs.String("deny-list", env(authn.DenyListEnv, ""), "deny list file")
	subject := fs.String("subject", "", "refuse every token of this subject issued up to now")
	id := fs.String("token-id", "", "refuse one token by its id (jti; see `identity show`)")
	fs.Parse(args)
	if *file == "" || (*subject == "") == (*id == "") {
		return errors.New("--deny-list and exactly one of --subject or --token-id are required")
	}
	if *subject != "" {
		if err := authn.RevokeSubject(*file, *subject, time.Now()); err != nil {
			return err
		}
		fmt.Printf("tokens of %s issued up to now are refused once %s reaches the services (within %s of the change)\n", *subject, *file, authn.DefaultDenyReload)
		return nil
	}
	if err := authn.RevokeToken(*file, *id); err != nil {
		return err
	}
	fmt.Printf("token %s is refused once %s reaches the services (within %s of the change)\n", *id, *file, authn.DefaultDenyReload)
	return nil
}

// show prints what a token file says, without checking the signature. Use it
// to find the id of a token to revoke.
func show(args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	file := fs.String("token", "", "token file")
	fs.Parse(args)
	b, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimSpace(string(b)), ".")
	if len(parts) != 3 {
		return errors.New("not a token file")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var p authn.Principal
	if err := json.Unmarshal(body, &p); err != nil {
		return err
	}
	fmt.Printf("subject %s\nid %s\nscopes %s\nissued %s\nexpires %s\n", p.Subject, p.ID, strings.Join(p.Scopes, ","),
		time.Unix(p.IssuedAt, 0).UTC().Format(time.RFC3339), time.Unix(p.Expires, 0).UTC().Format(time.RFC3339))
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", env("MAGI_LISTEN", ":8080"), "listen address")
	keys := fs.String("auth-keys", env("MAGI_AUTH_KEYS", ""), "comma-separated issuer key files to accept (the issuer key is always accepted)")
	issuerKey := fs.String("issuer-key", env("MAGI_ISSUER_KEY", ""), "issuer private key file")
	publicURL := fs.String("public-url", env("MAGI_PUBLIC_URL", ""), "public base URL, such as https://magi.example.local")
	domains := fs.String("allowed-domains", env("MAGI_ALLOWED_DOMAINS", ""), "comma-separated admitted identity domains")
	runtime := fs.String("linkkeys-runtime", env("MAGI_LINKKEYS_RUNTIME", ""), "private LinkKeys RP runtime JSON file")
	dbURL := fs.String("database-url", env("MAGI_DATABASE_URL", ""), "PostgreSQL URL")
	tokenTTL := fs.Duration("token-ttl", durEnv("MAGI_TOKEN_TTL", server.DefaultTokenTTL), "user token lifetime")
	sessionTTL := fs.Duration("session-ttl", durEnv("MAGI_SESSION_TTL", server.DefaultSessionTTL), "login session lifetime")
	appKeysFile := fs.String("app-keys", env("MAGI_APP_KEYS", ""), "optional LinkKeys application key file (act-as grants)")
	credKeyFile := fs.String("credential-key", env("MAGI_CREDENTIAL_KEY", ""), "optional credential encryption key file")
	fs.Parse(args)

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *issuerKey == "" || *publicURL == "" || *runtime == "" || *dbURL == "" || strings.TrimSpace(*domains) == "" {
		return errors.New("MAGI_ISSUER_KEY, MAGI_PUBLIC_URL, MAGI_ALLOWED_DOMAINS, MAGI_LINKKEYS_RUNTIME, and MAGI_DATABASE_URL are required")
	}
	pub := strings.TrimRight(*publicURL, "/")
	if !strings.HasPrefix(pub, "https://") && !strings.HasPrefix(pub, "http://localhost") && !strings.HasPrefix(pub, "http://127.0.0.1") {
		return errors.New("MAGI_PUBLIC_URL must use https")
	}
	iss, err := authn.LoadIssuer(*issuerKey)
	if err != nil {
		return err
	}
	paths := []string{*issuerKey}
	for _, p := range strings.Split(*keys, ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	v, err := authn.LoadVerifier(paths...)
	if err != nil {
		return err
	}
	rt, err := linkkeys.LoadRuntime(*runtime)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, d := range strings.Split(*domains, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			allowed[d] = true
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	go func() {
		for t := time.NewTicker(time.Hour); ; {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := db.Prune(ctx, time.Now().Add(-24*time.Hour).UnixMilli()); err != nil {
					log.Warn("prune", "err", err)
				}
			}
		}
	}()
	svc := &server.Service{
		DB: db, Issuer: iss, PublicURL: pub, Domains: allowed, TokenTTL: *tokenTTL, SessionTTL: *sessionTTL, Log: log,
		LinkKeys: &linkkeys.Client{RP: linkkeys.NewRpClient(rt.Transport()), RPDomain: rt.RPDomain},
	}
	if *appKeysFile != "" {
		g, err := grantee(*appKeysFile, rt)
		if err != nil {
			return err
		}
		svc.Grantee = g
		go func() {
			// Renew the act-as grants before they expire (RenewGrants).
			for t := time.NewTicker(15 * time.Minute); ; {
				if n, err := svc.RenewGrants(ctx); err != nil {
					log.Error("grant renewal failed", "err", err)
				} else if n > 0 {
					log.Info("grants renewed", "grants", n)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
		go func() {
			for t := time.NewTicker(6 * time.Hour); ; {
				if err := g.RenewKeys(ctx); err != nil {
					log.Error("application key attestation renewal failed", "err", err)
				} else {
					log.Info("application key attestations renewed", "keys", len(g.Keys.Keys))
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	if *credKeyFile != "" {
		if svc.CredentialKey, err = server.LoadCredentialKey(*credKeyFile); err != nil {
			return err
		}
	}
	s := &rpc.Server{Verifier: v, Log: log, MaxBody: 64 << 10}
	server.Register(s, svc)
	mux := rpc.Mux(s, func() error { return db.Ping(ctx) })
	svc.Routes(mux)
	log.Info("identity listening", "addr", *listen, "public_url", pub, "rp_domain", rt.RPDomain, "domains", *domains)
	return rpc.ListenAndServe(ctx, *listen, http.Handler(mux))
}

func durEnv(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil {
		return d
	}
	return def
}

// stopMachine refuses renewal to every current token of a machine subject.
// The tokens stay valid until they expire (at most server.MachineTokenTTL).
func stopMachine(args []string) error {
	fs := flag.NewFlagSet("stop-machine", flag.ExitOnError)
	subject := fs.String("subject", "", "inference:<name> or runner:<name>")
	dbURL := fs.String("database-url", env("MAGI_DATABASE_URL", ""), "PostgreSQL URL")
	deny := fs.String("deny-list", "", "deny list file: also refuse the current tokens of the subject (see revoke)")
	fs.Parse(args)
	if !strings.HasPrefix(*subject, "inference:") && !strings.HasPrefix(*subject, "runner:") {
		return errors.New("subject must start with inference: or runner:")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.StopMachine(ctx, *subject, time.Now().UnixMilli()); err != nil {
		return err
	}
	if *deny != "" {
		if err := authn.RevokeSubject(*deny, *subject, time.Now()); err != nil {
			return err
		}
		fmt.Printf("renewal stopped for %s; its current tokens are refused once %s reaches the services\n", *subject, *deny)
		return nil
	}
	fmt.Printf("renewal stopped for %s; its tokens expire in at most %s (use --deny-list to refuse them sooner)\n", *subject, server.MachineTokenTTL)
	return nil
}

// grantee makes the act-as grantee from the key file and the RP runtime.
// The application account lives on the RP's domain, so the RP server is
// also the home domain for attestation renewal.
func grantee(path string, rt linkkeys.Runtime) (*server.Grantee, error) {
	keys, err := server.LoadAppKeys(path)
	if err != nil {
		return nil, err
	}
	rpT, err := rp.NewPinnedRpcTransport(rp.PinnedRpcTransportOptions{TCPAddress: rt.TCPAddress, Fingerprints: rt.Fingerprints, APIKey: rt.APIKey})
	if err != nil {
		return nil, err
	}
	home, err := rp.NewPinnedRpcTransport(rp.PinnedRpcTransportOptions{TCPAddress: rt.TCPAddress, Fingerprints: rt.Fingerprints, APIKey: "application-signature"})
	if err != nil {
		return nil, err
	}
	return server.NewGrantee(keys, rpT, home), nil
}

func appKeys(args []string) error {
	if len(args) == 0 {
		return errors.New("app-keys generate|enroll|renew")
	}
	action, args := args[0], args[1:]
	fs := flag.NewFlagSet("app-keys", flag.ExitOnError)
	file := fs.String("file", "", "application key file")
	domain := fs.String("domain", "", "account domain (generate)")
	userID := fs.String("user-id", "", "account user id (generate)")
	appID := fs.String("app-id", "magi", "application id (generate)")
	instance := fs.String("instance-id", "", "instance id (generate)")
	runtime := fs.String("linkkeys-runtime", env("MAGI_LINKKEYS_RUNTIME", ""), "LinkKeys runtime file; its API key must belong to the application account")
	tcp := fs.String("tcp-addr", "", "override the runtime TCP address (for example the public LinkKeys address)")
	fs.Parse(args)
	if *file == "" {
		return errors.New("--file is required")
	}
	if action == "generate" {
		if err := server.GenerateAppKeys(*file, *domain, *userID, *appID, *instance); err != nil {
			return err
		}
		fmt.Println("application keys written to", *file)
		return nil
	}
	rt, err := linkkeys.LoadRuntime(*runtime)
	if err != nil {
		return err
	}
	if *tcp != "" {
		rt.TCPAddress = *tcp
	}
	g, err := grantee(*file, rt)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch action {
	case "enroll":
		account, err := rp.NewPinnedRpcTransport(rp.PinnedRpcTransportOptions{TCPAddress: rt.TCPAddress, Fingerprints: rt.Fingerprints, APIKey: rt.APIKey})
		if err != nil {
			return err
		}
		if err := g.Enroll(ctx, account); err != nil {
			return err
		}
		a := g.Keys.Application
		fmt.Printf("enrolled %s@%s/%s instance %s\n", a.SubjectUserID, a.SubjectDomain, a.ApplicationID, g.Keys.InstanceID)
	case "renew":
		if err := g.RenewKeys(ctx); err != nil {
			return err
		}
		fmt.Println("attestations renewed")
	default:
		return fmt.Errorf("unknown app-keys action %q", action)
	}
	return nil
}

func credentialKey(args []string) error {
	fs := flag.NewFlagSet("credential-key", flag.ExitOnError)
	out := fs.String("out", "", "key file to create (mode 600)")
	fs.Parse(args)
	if *out == "" {
		return errors.New("--out is required")
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return err
	}
	if err := writePrivate(*out, []byte(base64.StdEncoding.EncodeToString(k)+"\n")); err != nil {
		return err
	}
	fmt.Println("credential key written to", *out)
	return nil
}
