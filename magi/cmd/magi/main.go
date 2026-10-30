// Command magi is a command-line client of the Magi API.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	identityapi "github.com/catalystcommunity/magi/primitives/identity/api"
	"golang.org/x/term"
)

const usage = `usage: magi [--server URL] <command> [args]

  login USER@DOMAIN          sign in with LinkKeys in a browser on any
                             device, then paste the code it shows
  whoami                     show the signed-in identity
  chat [CHAT]                talk interactively (text); new chat if none
  say [-chat C] [-audio F] [-speak] [TEXT]
                             send one message and wait for the reply
  chats                      list chats
  read CHAT [-follow]        print messages of a chat
  tasks                      list tasks
  task ID                    show one task
  diff ID                    show the change of a task
  decide ID OUTCOME [-m TEXT]   approve, revise, or reject a waiting task
  cancel ID [-m TEXT] [-yes]  stop a task; asks first when the step is risky
  start [-repo R] [-title T] INSTRUCTIONS   start a code change directly
  know [TEXT]                search your knowledge
  remember KIND LABEL [-summary S] [-system S] [-locator L] [-related R -relation N]
  audio DIGEST -o FILE       save the audio of a message
  voices                     list voices
  usage [-days N]            count Magi replies by model (default 7 days)

The server comes from --server, MAGI_SERVER, or the login profile.
MAGI_TOKEN_FILE selects a token file instead of the login profile.`

type profile struct {
	Server  string `json:"server"`
	Token   string `json:"token"`
	Subject string `json:"subject"`
}

func profilePath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "magi", "cli.json")
}

func loadProfile() profile {
	var p profile
	if b, err := os.ReadFile(profilePath()); err == nil {
		json.Unmarshal(b, &p)
	}
	if f := os.Getenv("MAGI_TOKEN_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			p.Token = strings.TrimSpace(string(b))
		}
	}
	return p
}

func main() {
	server := flag.String("server", os.Getenv("MAGI_SERVER"), "Magi base URL")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	p := loadProfile()
	if *server != "" {
		p.Server = *server
	}
	if p.Server == "" {
		fmt.Fprintln(os.Stderr, "magi: no server; use --server or MAGI_SERVER")
		os.Exit(2)
	}
	c := &cli{p: p, ctx: context.Background()}
	t := rpc.NewClient(p.Server, func() string { return c.p.Token })
	c.magi, c.id = magiapi.NewMagiClient(t), identityapi.NewIdentityClient(t)
	if err := c.run(flag.Arg(0), flag.Args()[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "magi:", err)
		os.Exit(1)
	}
}

type cli struct {
	p    profile
	ctx  context.Context
	magi *magiapi.MagiClient
	id   *identityapi.IdentityClient
}

func ptr[T any](v T) *T { return &v }

func (c *cli) run(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "login":
		if len(args) != 1 {
			return errors.New("login USER@DOMAIN")
		}
		return c.login(args[0])
	case "whoami":
		w, err := c.id.WhoAmI(c.ctx, identityapi.WhoAmIRequest{})
		if err != nil {
			return err
		}
		fmt.Printf("%s (%s), expires %s\n", w.Subject, w.Kind, time.UnixMilli(int64(w.ExpiresMs)).Format(time.RFC1123))
	case "chat":
		chat := ""
		if len(args) > 0 {
			chat = args[0]
		}
		return c.chat(chat)
	case "say":
		chat, audio, speak := fs.String("chat", "", "chat id"), fs.String("audio", "", "audio file to send"), fs.Bool("speak", false, "ask for a spoken reply")
		fs.Parse(args)
		req := magiapi.SayRequest{Chat: optional(*chat), SpeakReply: speak}
		if t := strings.Join(fs.Args(), " "); t != "" {
			req.Text = &t
		}
		if *audio != "" {
			b, err := os.ReadFile(*audio)
			if err != nil {
				return err
			}
			req.Audio, req.AudioType = &b, ptr(audioType(*audio))
		}
		res, err := c.magi.Say(c.ctx, req)
		if err != nil {
			return err
		}
		fmt.Printf("chat %s\n", res.Chat)
		printMessage(res.Message)
		return c.waitReply(res.Chat, res.Message)
	case "chats":
		res, err := c.magi.ListChats(c.ctx, magiapi.ListChatsRequest{})
		if err != nil {
			return err
		}
		for _, ch := range res.Chats {
			fmt.Printf("%s  %s  %s\n", ch.Id, time.UnixMilli(int64(ch.UpdatedMs)).Format("2006-01-02 15:04"), ch.Title)
		}
	case "usage":
		days := fs.Int("days", 7, "days to count")
		fs.Parse(args)
		return c.usage(time.Now().Add(-time.Duration(*days) * 24 * time.Hour))
	case "read":
		follow := fs.Bool("follow", false, "wait for new messages")
		fs.Parse(args)
		if fs.NArg() != 1 {
			return errors.New("read CHAT")
		}
		return c.read(fs.Arg(0), 0, *follow, nil)
	case "tasks":
		res, err := c.magi.ListTasks(c.ctx, magiapi.ListTasksRequest{})
		if err != nil {
			return err
		}
		for _, t := range res.Tasks {
			fmt.Printf("%s  %-10s %-8s %s\n", t.Id, t.Status, t.CurrentStep, t.Title)
		}
	case "task":
		if len(args) != 1 {
			return errors.New("task ID")
		}
		t, err := c.magi.GetTask(c.ctx, magiapi.GetTaskRequest{Id: args[0]})
		if err != nil {
			return err
		}
		printTask(t)
	case "diff":
		if len(args) != 1 {
			return errors.New("diff ID")
		}
		d, err := c.magi.GetTaskDiff(c.ctx, magiapi.GetTaskDiffRequest{Id: args[0]})
		if err != nil {
			return err
		}
		for _, f := range d.Files {
			fmt.Printf("%5d+ %5d-  %s\n", f.Additions, f.Deletions, f.Path)
		}
		fmt.Println()
		fmt.Print(d.Patch)
	case "decide":
		comment := fs.String("m", "", "comment")
		if len(args) < 2 {
			return errors.New("decide ID OUTCOME [-m TEXT]")
		}
		fs.Parse(args[2:])
		t, err := c.magi.Decide(c.ctx, magiapi.DecideRequest{Task: args[0], Outcome: args[1], Comment: optional(*comment)})
		if err != nil {
			return err
		}
		printTask(t)
	case "cancel":
		reason, yes := fs.String("m", "", "reason"), fs.Bool("yes", false, "accept the risk of a risky step without a question")
		if len(args) < 1 {
			return errors.New("cancel ID [-m TEXT] [-yes]")
		}
		fs.Parse(args[1:])
		req := magiapi.CancelTaskRequest{Task: args[0], Reason: optional(*reason), Confirm: yes}
		t, err := c.magi.CancelTask(c.ctx, req)
		if msg, ok := strings.CutPrefix(rpcMessage(err), "confirm: "); ok && !*yes {
			fmt.Printf("Stopping this task is risky: %s\nStop it anyway? [y/N] ", msg)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
				return errors.New("not stopped")
			}
			req.Confirm = ptr(true)
			t, err = c.magi.CancelTask(c.ctx, req)
		}
		if err != nil {
			return err
		}
		printTask(t)
	case "start":
		repo, title := fs.String("repo", "", "repository"), fs.String("title", "", "title")
		fs.Parse(args)
		t, err := c.magi.StartTask(c.ctx, magiapi.StartTaskRequest{Kind: "code-change", Instructions: strings.Join(fs.Args(), " "), Repo: optional(*repo), Title: optional(*title)})
		if err != nil {
			return err
		}
		printTask(t)
	case "know":
		res, err := c.magi.QueryKnowledge(c.ctx, magiapi.QueryKnowledgeRequest{Text: optional(strings.Join(args, " "))})
		if err != nil {
			return err
		}
		for _, it := range res.Items {
			fmt.Printf("%s  [%s] %s: %s\n", it.Id, it.Kind, it.Label, it.Summary)
			if it.System != nil {
				fmt.Printf("      in %s at %s\n", *it.System, deref(it.Locator))
			}
			for _, r := range it.Relations {
				fmt.Printf("      %s\n", r)
			}
		}
	case "remember":
		summary, system, locator := fs.String("summary", "", ""), fs.String("system", "", ""), fs.String("locator", "", "")
		related, relation := fs.String("related", "", ""), fs.String("relation", "", "")
		if len(args) < 2 {
			return errors.New("remember KIND LABEL [flags]")
		}
		fs.Parse(args[2:])
		it, err := c.magi.Remember(c.ctx, magiapi.RememberRequest{Kind: args[0], Label: args[1], Summary: optional(*summary),
			System: optional(*system), Locator: optional(*locator), RelatedTo: optional(*related), Relation: optional(*relation)})
		if err != nil {
			return err
		}
		fmt.Printf("%s  [%s] %s\n", it.Id, it.Kind, it.Label)
	case "audio":
		out := fs.String("o", "", "output file")
		if len(args) < 1 {
			return errors.New("audio DIGEST -o FILE")
		}
		fs.Parse(args[1:])
		a, err := c.magi.GetAudio(c.ctx, magiapi.GetAudioRequest{Digest: args[0]})
		if err != nil {
			return err
		}
		if *out == "" {
			return errors.New("-o FILE is required")
		}
		return os.WriteFile(*out, a.Body, 0o644)
	case "voices":
		res, err := c.magi.ListVoices(c.ctx, magiapi.ListVoicesRequest{})
		if err != nil {
			return err
		}
		for _, v := range res.Voices {
			fmt.Printf("%-12s %-10s %s\n", v.Id, v.Language, v.Name)
		}
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

// login signs in with a browser on any machine. The browser shows a code
// after the sign in, and the person pastes it here.
func (c *cli) login(identity string) error {
	b, err := c.id.BeginLogin(c.ctx, identityapi.BeginLoginRequest{Identity: identity, Client: ptr("cli")})
	if err != nil {
		return err
	}
	fmt.Printf("Open this URL in a browser on any device and sign in:\n\n  %s\n\n", b.Url)
	if !headless() {
		openBrowser(b.Url)
	}
	// Poll without the code to learn when the browser part ends or fails.
	for {
		if time.Now().UnixMilli() >= int64(b.ExpiresMs) {
			return errors.New("login expired")
		}
		r, err := c.id.PollLogin(c.ctx, identityapi.PollLoginRequest{LoginId: b.LoginId, WaitMs: ptr(uint64(25000))})
		if err != nil {
			return err
		}
		if r.Status == "needs_code" {
			break
		}
		if r.Status != "pending" {
			return fmt.Errorf("login %s: %s", r.Status, deref(r.Message))
		}
	}
	code, err := readSecret("Paste the code from the browser (it is not shown): ")
	if err != nil {
		return err
	}
	if n := len(code); n >= 6 {
		fmt.Printf("Code ending in %s.\n", code[n-6:])
	}
	r, err := c.id.PollLogin(c.ctx, identityapi.PollLoginRequest{LoginId: b.LoginId, Code: &code})
	if err != nil {
		return err
	}
	if r.Status != "complete" || r.Token == nil {
		return fmt.Errorf("login %s: %s", r.Status, deref(r.Message))
	}
	c.p.Token, c.p.Subject = deref(r.Token), deref(r.Subject)
	if err := os.MkdirAll(filepath.Dir(profilePath()), 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(c.p)
	if err := os.WriteFile(profilePath(), data, 0o600); err != nil {
		return err
	}
	fmt.Printf("Signed in as %s.\n", c.p.Subject)
	return nil
}

// readSecret reads one line without echo when stdin is a terminal.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// headless reports whether no local browser is likely, for example in SSH.
// Then the program does not try to open one.
func headless() bool {
	if os.Getenv("SSH_CONNECTION") != "" {
		return true
	}
	return runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}

// waitReply prints messages after sent until a persona reply to it arrives.
func (c *cli) waitReply(chat string, sent magiapi.Message) error {
	done := false
	return c.read(chat, sent.Seq, true, func(m magiapi.Message) bool {
		if m.AuthorKind == "persona" && deref(m.ReplyTo) == sent.Id {
			done = true
		}
		return done
	})
}

// read prints messages after seq. With follow, it waits for more until
// stop returns true.
func (c *cli) read(chat string, after uint64, follow bool, stop func(magiapi.Message) bool) error {
	for {
		res, err := c.magi.Read(c.ctx, magiapi.ReadRequest{Chat: chat, AfterSeq: ptr(after), WaitMs: ptr(uint64(25000))})
		if err != nil {
			if rpc.CodeOf(err) == 0 {
				time.Sleep(2 * time.Second)
				continue
			}
			return err
		}
		for _, m := range res.Messages {
			printMessage(m)
			after = m.Seq
			if stop != nil && stop(m) {
				return nil
			}
		}
		if !follow && len(res.Messages) == 0 {
			return nil
		}
	}
}

// usage counts the persona replies since a time by the model that wrote
// them. It reads through the API, so it sees only the caller's chats.
func (c *cli) usage(since time.Time) error {
	from := uint64(since.UnixMilli())
	chats, err := c.magi.ListChats(c.ctx, magiapi.ListChatsRequest{Limit: ptr(uint64(1000))})
	if err != nil {
		return err
	}
	count, total := map[string]int{}, 0
	for _, ch := range chats.Chats {
		if ch.UpdatedMs < from {
			continue
		}
		for after := uint64(0); ; {
			res, err := c.magi.Read(c.ctx, magiapi.ReadRequest{Chat: ch.Id, AfterSeq: ptr(after), Limit: ptr(uint64(1000))})
			if err != nil {
				return err
			}
			for _, m := range res.Messages {
				after = m.Seq
				if m.AuthorKind == "persona" && m.CreatedMs >= from {
					count[deref(m.GeneratedBy)]++
					total++
				}
			}
			if len(res.Messages) == 0 || after >= res.LastSeq {
				break
			}
		}
	}
	if total == 0 {
		fmt.Println("no replies since", since.Format("2006-01-02 15:04"))
		return nil
	}
	models := make([]string, 0, len(count))
	for m := range count {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return count[models[i]] > count[models[j]] })
	for _, m := range models {
		name := m
		if name == "" {
			name = "(not recorded)"
		}
		fmt.Printf("%-40s %6d %5.1f%%\n", name, count[m], 100*float64(count[m])/float64(total))
	}
	return nil
}

func (c *cli) chat(chat string) error {
	in := bufio.NewScanner(os.Stdin)
	if chat == "" {
		fmt.Println("New chat. Type a message; an empty line or Ctrl-D ends.")
	} else {
		fmt.Println("Chat", chat)
	}
	for {
		fmt.Print("> ")
		if !in.Scan() || strings.TrimSpace(in.Text()) == "" {
			return in.Err()
		}
		text := in.Text()
		res, err := c.magi.Say(c.ctx, magiapi.SayRequest{Chat: optional(chat), Text: &text})
		if err != nil {
			fmt.Fprintln(os.Stderr, "magi:", err)
			continue
		}
		chat = res.Chat
		if err := c.waitReply(chat, res.Message); err != nil {
			fmt.Fprintln(os.Stderr, "magi:", err)
		}
	}
}

func printMessage(m magiapi.Message) {
	who := m.Author
	if m.AuthorKind == "persona" {
		who = "Magi"
	}
	extra := ""
	if m.GeneratedBy != nil {
		extra = " (" + *m.GeneratedBy + ")"
	}
	if m.Audio != nil {
		extra += " [audio " + *m.Audio + "]"
	}
	fmt.Printf("\n%s %s%s:\n%s\n", time.UnixMilli(int64(m.CreatedMs)).Format("15:04:05"), who, extra, m.Text)
	for _, r := range m.Refs {
		fmt.Printf("  -> %s %s %s\n", r.Kind, r.Ref, deref(r.Title))
	}
}

func printTask(t magiapi.Task) {
	fmt.Printf("%s  %s\n  status %s, step %s, branch %s\n", t.Id, t.Title, t.Status, t.CurrentStep, deref(t.Branch))
	if t.WaitingFor != nil {
		fmt.Printf("  waiting for a decision (%s): %s\n", strings.Join(t.Decisions, ", "), *t.WaitingFor)
	}
	if t.Error != nil {
		fmt.Printf("  error: %s\n", *t.Error)
	}
	for _, s := range t.Steps {
		fmt.Printf("  - %-8s %-9s %s\n", s.Step, s.Status, deref(s.Outcome))
		for _, n := range s.Notes {
			fmt.Printf("      %s\n", n)
		}
	}
	for _, r := range t.Refs {
		fmt.Printf("  ref %s %s (%s)\n", r.Kind, r.Ref, deref(r.Title))
	}
}

func audioType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".webm":
		return "audio/webm"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	}
	return "audio/wav"
}

// rpcMessage returns the message of an RPC error, or "".
func rpcMessage(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return ""
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}
