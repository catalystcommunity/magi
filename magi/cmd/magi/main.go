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
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	identityapi "github.com/catalystcommunity/magi/primitives/identity/api"
	"golang.org/x/term"
)

var usage = `usage: magi [--server URL] <command> [args]

  login USER@DOMAIN          sign in with LinkKeys in a browser on any
                             device, then paste the code it shows
  whoami                     show the signed-in identity
  chat [-persona P]          talk interactively (text). There are no chats:
                             each person has one stream with each persona
                             (default persona:magi)
  say [-persona P] [-audio F] [-speak] [-wake] [TEXT]
                             send one message and wait for the reply; -wake:
                             only when it starts with a persona name
                             ("Computer, ...")
  read [-persona P] [-n N] [-follow]   print the newest N messages (default 20)
  search TEXT                find messages that you can read
  trace ID [-forward] [-depth N]   follow the links of a message
  links ID [-all]            show the links of a message (audit)
  link ID KIND TARGET [-replaces L] [-m NOTE]
                             add a link from message ID to message TARGET
                             (or KIND:REF, for example workflow-run:run_x)
  unlink LINK [-m NOTE]      retract a link
  visibility ID private|shared [-m NOTE]   correct who can read a message
  reviews                    list the questions of the librarian
  answer REVIEW ANSWER [-m NOTE]   answer one (a choice, a link kind, or dismiss)
  personas                   list personas
  persona ID NAME never|private|public [MEMBER...]   operator: make or change a persona
  tasks                      list tasks
  connect calendar|longhouse      get a link that lets Magi act for you there
  import-history [-persona P] [-from N] FILE   import your earlier messages with their times
  activity [-persona P]           what Magi does now for you (a reply in progress)
  connections                     list your connections; disconnect SYSTEM forgets one
  todos [-all] [-plan P] [-json]  list your to-dos and reminders (-all: also the
                             ones that ended in the last 7 days)
  todo ID done|skip|cancel   finish a to-do or cancel a reminder
  task ID                    show one task
  diff ID                    show the change of a task
  output ID check|transcript   show the last check output or apprentice record
  decide ID OUTCOME [-m TEXT]   approve, revise, or reject a waiting task
  cancel ID [-m TEXT] [-yes]  stop a task; asks first when the step is risky
  start [-repo R] [-title T] [-runner N] INSTRUCTIONS   start a code change directly
  know [-past] [TEXT]        search your knowledge (-past: also facts that ended)
  remember SUBJECT RELATION [OBJECT] [-value V] [-subject-kind K] [-object-kind K] [-also]
                             record a fact; SUBJECT "me" is you, for example
                             remember me dentist "Dr. Rivera" -object-kind person
  fix ID VALUE [-field F] [-m REASON]      the record was wrong: change it in place
  update ID VALUE [-field F] [-m REASON]   the fact was true and changed: end it
                             and record the new value
  retract ID [-m REASON] [-erase]   the record was never true, or forget it
                             (-erase: keep no copy in the history)
  merge KEEP MERGE [-m REASON]   two entities are the same thing: move the
                             facts of MERGE to KEEP (unmerge CHANGE reverts it)
  history ID                 the changes of a record
  audio DIGEST -o FILE       save the audio of a message
  voices                     list voices
  words                      list the household lexicon (how Magi says and
                             hears words)
  word WRITTEN [-say S] [-ipa P] [-heard A,B] [-m NOTE]
                             lexicon editor: add or change a word. -say is
                             the text to say, -ipa misaki phonemes, -heard
                             forms that speech to text writes for the word
  unword WRITTEN             lexicon editor: remove a word
  speak TEXT [-voice V] [-o FILE]   show the spoken text; -o saves the audio
  egress-reviews [-state open|decided]
                             admin: requests that the egress proxy blocked for review
  egress-review ID allow-host|allow-path|allow-layer4|block|dismiss [-m NOTE]
                             admin: decide a review; the decision is a rule for
                             the next requests (dismiss: no rule); a new decision
                             of a decided review replaces its rule
  usage [-days N]            count Magi replies by model (default 7 days)
  runners                    list connected runners and the runner release
  runner-release VERSION DIR   operator: upload DIR/magi-runner-<os>-<arch>.tar.gz
                             (from go run ./cmd/runner-package) and make VERSION
                             the release; runners update when idle
  app-release CODE NAME APK  operator: publish an Android app build
` + attentionUsage[1:] + `

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
	spin *spinner // while a reply is built (waitReply)
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
		persona := fs.String("persona", "", "persona")
		fs.Parse(args)
		return c.chat(personaID(*persona))
	case "say":
		persona, audio, speak := fs.String("persona", "", "persona"), fs.String("audio", "", "audio file to send"), fs.Bool("speak", false, "ask for a spoken reply")
		wake := fs.Bool("wake", false, "only when the message starts with a persona name")
		fs.Parse(args)
		req := magiapi.SayRequest{Persona: optional(personaID(*persona)), SpeakReply: speak, WakeWord: wake}
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
		if deref(res.Ignored) {
			fmt.Println("(not for Magi: nothing was kept)")
			return nil
		}
		printMessage(res.Message)
		// A message that names a persona ("Engineering, ...") goes to it.
		return c.waitReply(firstNonEmpty(res.Message.Persona, personaID(*persona)), res.Message)
	case "search":
		res, err := c.magi.Search(c.ctx, magiapi.SearchRequest{Text: strings.Join(args, " ")})
		if err != nil {
			return err
		}
		for _, m := range res.Messages {
			printMessage(m)
		}
	case "trace":
		fwd, depth := fs.Bool("forward", false, "follow links to the message"), fs.Uint64("depth", 2, "depth")
		if len(args) < 1 {
			return errors.New("trace ID")
		}
		fs.Parse(args[1:])
		dir := "back"
		if *fwd {
			dir = "forward"
		}
		res, err := c.magi.Trace(c.ctx, magiapi.TraceRequest{Message: args[0], Direction: &dir, Depth: depth})
		if err != nil {
			return err
		}
		for _, m := range res.Messages {
			printMessage(m)
		}
	case "links":
		all := fs.Bool("all", false, "also tentative, replaced, and retracted links")
		if len(args) < 1 {
			return errors.New("links ID")
		}
		fs.Parse(args[1:])
		res, err := c.magi.ListLinks(c.ctx, magiapi.ListLinksRequest{Message: args[0], All: all})
		if err != nil {
			return err
		}
		for _, l := range res.Links {
			printLink(l)
		}
	case "link":
		replaces, note := fs.String("replaces", "", "link that this one replaces"), fs.String("m", "", "note")
		if len(args) < 3 {
			return errors.New("link ID KIND TARGET")
		}
		fs.Parse(args[3:])
		target := magiapi.Ref{Kind: "utterance", Ref: args[2]}
		if k, r, ok := strings.Cut(args[2], ":"); ok && !strings.HasPrefix(args[2], "utt_") {
			target = magiapi.Ref{Kind: k, Ref: r}
		}
		l, err := c.magi.Link(c.ctx, magiapi.LinkRequest{Message: args[0], Kind: args[1], Target: target, Replaces: optional(*replaces), Note: optional(*note)})
		if err != nil {
			return err
		}
		printLink(l)
	case "unlink":
		note := fs.String("m", "", "note")
		if len(args) < 1 {
			return errors.New("unlink LINK")
		}
		fs.Parse(args[1:])
		l, err := c.magi.Unlink(c.ctx, magiapi.UnlinkRequest{Link: args[0], Note: optional(*note)})
		if err != nil {
			return err
		}
		printLink(l)
	case "visibility":
		note := fs.String("m", "", "note")
		if len(args) < 2 {
			return errors.New("visibility ID private|shared")
		}
		fs.Parse(args[2:])
		m, err := c.magi.SetVisibility(c.ctx, magiapi.SetVisibilityRequest{Message: args[0], Visibility: args[1], Note: optional(*note)})
		if err != nil {
			return err
		}
		printMessage(m)
	case "reviews":
		res, err := c.magi.ListReviews(c.ctx, magiapi.ListReviewsRequest{})
		if err != nil {
			return err
		}
		if len(res.Reviews) == 0 {
			fmt.Println("no open reviews")
		}
		for _, r := range res.Reviews {
			fmt.Printf("\n%s  %s (confidence %.2f)\n  %s\n  answers: %s\n", r.Id, r.Task, r.Confidence, r.Question, strings.Join(r.Choices, ", "))
			fmt.Printf("  message %s: %s\n", r.Message.Id, oneLine(r.Message.Text, 100))
			if r.Target != nil {
				fmt.Printf("  earlier %s: %s\n", r.Target.Id, oneLine(r.Target.Text, 100))
			}
		}
	case "answer":
		note := fs.String("m", "", "note: why (the librarian learns from it)")
		if len(args) < 2 {
			return errors.New("answer REVIEW ANSWER")
		}
		fs.Parse(args[2:])
		res, err := c.magi.AnswerReview(c.ctx, magiapi.AnswerReviewRequest{Id: args[0], Answer: args[1], Note: optional(*note)})
		if err != nil {
			return err
		}
		fmt.Println(res.Status)
	case "personas":
		res, err := c.magi.ListPersonas(c.ctx, magiapi.ListPersonasRequest{})
		if err != nil {
			return err
		}
		for _, p := range res.Personas {
			fmt.Printf("%-20s %-12s %-8s %s\n", p.Id, p.Name, p.Sharing, strings.Join(p.Members, ","))
		}
	case "persona":
		if len(args) < 3 {
			return errors.New("persona ID NAME never|private|public [MEMBER...]")
		}
		p, err := c.magi.PutPersona(c.ctx, magiapi.PutPersonaRequest{Id: args[0], Name: args[1], Sharing: args[2], Members: args[3:]})
		if err != nil {
			return err
		}
		fmt.Printf("%s %s %s %s\n", p.Id, p.Name, p.Sharing, strings.Join(p.Members, ","))
	case "runners":
		res, err := c.magi.ListRunners(c.ctx, magiapi.ListRunnersRequest{})
		if err != nil {
			return err
		}
		fmt.Printf("runner release: %s\n", deref(res.Release))
		for _, r := range res.Runners {
			state := "idle"
			if r.Busy {
				state = "busy"
			}
			fmt.Printf("%-16s %-14s %s/%s  %s  seen %s  repos %s  publishes %s\n", r.Name, r.Version, r.Os, r.Arch, state,
				time.UnixMilli(int64(r.SeenMs)).Format("15:04:05"), strings.Join(r.Repos, ","), strings.Join(r.Publishes, ","))
			if e := deref(r.UpdateError); e != "" {
				fmt.Printf("%-16s %s\n", "", e)
			}
		}
	case "runner-release":
		if len(args) != 2 {
			return errors.New("runner-release VERSION DIR")
		}
		files, _ := filepath.Glob(filepath.Join(args[1], "magi-runner-*-*.tar.gz"))
		if len(files) == 0 {
			return fmt.Errorf("no magi-runner-<os>-<arch>.tar.gz in %s", args[1])
		}
		for _, f := range files {
			plat := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "magi-runner-"), ".tar.gz")
			goos, arch, _ := strings.Cut(plat, "-")
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			res, err := c.magi.PutRunnerPackage(c.ctx, magiapi.PutRunnerPackageRequest{Version: args[0], Os: goos, Arch: arch, Body: b})
			if err != nil {
				return err
			}
			fmt.Printf("%s/%s %s\n", goos, arch, res.Digest)
		}
		res, err := c.magi.SetRunnerRelease(c.ctx, magiapi.SetRunnerReleaseRequest{Version: args[0]})
		if err != nil {
			return err
		}
		fmt.Printf("runner release %s: %s\n", res.Version, strings.Join(res.Packages, ", "))
	case "app-release":
		if len(args) != 3 {
			return errors.New("app-release VERSION_CODE VERSION_NAME APK")
		}
		code, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		rel, err := c.magi.PutAppRelease(c.ctx, magiapi.PutAppReleaseRequest{VersionCode: code, VersionName: args[1], Body: b})
		if err != nil {
			return err
		}
		fmt.Printf("app release %d (%s) %s, %d bytes\n", rel.VersionCode, rel.VersionName, rel.Digest, rel.Size)
	case "usage":
		days := fs.Int("days", 7, "days to count")
		fs.Parse(args)
		return c.usage(time.Now().Add(-time.Duration(*days) * 24 * time.Hour))
	case "read":
		persona, n, follow := fs.String("persona", "", "persona"), fs.Uint64("n", 20, "messages"), fs.Bool("follow", false, "wait for new messages")
		fs.Parse(args)
		res, err := c.magi.Read(c.ctx, magiapi.ReadRequest{Persona: optional(personaID(*persona)), BeforeSeq: ptr(uint64(1) << 62), Limit: n})
		if err != nil {
			return err
		}
		for _, m := range res.Messages {
			printMessage(m)
		}
		if !*follow {
			return nil
		}
		return c.read(personaID(*persona), res.LastSeq, true, nil)
	case "connect":
		if len(args) != 1 {
			return errors.New("usage: magi connect calendar|longhouse")
		}
		r, err := c.magi.Connect(c.ctx, magiapi.ConnectRequest{System: args[0]})
		if err != nil {
			return err
		}
		fmt.Println(r.Message)
		fmt.Println(r.Url)
		return nil
	case "import-history":
		// The file is {"messages": [{"text": ..., "at_ms": ...}, ...]}.
		fs := flag.NewFlagSet("import-history", flag.ContinueOnError)
		p := fs.String("persona", "", "persona")
		from := fs.Int("from", 0, "first message to import (to continue a stopped import)")
		batch := fs.Int("batch", 10, "messages in one call (at most 20)")
		asJSON := fs.Bool("json", false, "print one JSON line for each message")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: magi import-history [-persona P] [-from N] FILE")
		}
		b, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		var f struct {
			Messages []magiapi.HistoryMessage `json:"messages"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return err
		}
		for i := *from; i < len(f.Messages); i += *batch {
			part := f.Messages[i:min(i+*batch, len(f.Messages))]
			r, err := c.magi.ImportHistory(c.ctx, magiapi.ImportHistoryRequest{Persona: optional(personaID(*p)), Messages: part})
			if err != nil {
				return fmt.Errorf("messages %d to %d: %w (continue with -from %d)", i, i+len(part)-1, err, i)
			}
			for j, m := range r.Messages {
				if *asJSON {
					line, _ := json.Marshal(map[string]any{"n": i + j, "id": m.Id, "at_ms": m.AtMs, "ms": m.Ms, "notes": m.Notes, "error": deref(m.Error)})
					fmt.Println(string(line))
				} else {
					fmt.Printf("%d %s %d ms %d notes %s\n", i+j, time.UnixMilli(int64(m.AtMs)).Format("2006-01-02"), m.Ms, len(m.Notes), deref(m.Error))
				}
			}
		}
		return nil
	case "activity":
		fs := flag.NewFlagSet("activity", flag.ContinueOnError)
		p := fs.String("persona", "", "only this persona")
		if err := fs.Parse(args); err != nil {
			return err
		}
		a, err := c.magi.Activity(c.ctx, magiapi.ActivityRequest{Persona: optional(personaID(*p))})
		if err != nil {
			return err
		}
		if !a.Working {
			fmt.Println("idle")
			return nil
		}
		fmt.Printf("%s is %s\n", deref(a.Persona), deref(a.Text))
		return nil
	case "connections":
		r, err := c.magi.ListConnections(c.ctx, magiapi.ListConnectionsRequest{})
		if err != nil {
			return err
		}
		for _, x := range r.Connections {
			line := x.System + " " + x.State
			if x.Detail != nil && *x.Detail != "" {
				line += " (" + *x.Detail + ")"
			}
			if x.ExpiresMs != nil && *x.ExpiresMs > 0 {
				line += " until " + time.UnixMilli(int64(*x.ExpiresMs)).Format("2006-01-02 15:04")
			}
			fmt.Println(line)
		}
		return nil
	case "disconnect":
		if len(args) != 1 {
			return errors.New("usage: magi disconnect calendar|longhouse")
		}
		r, err := c.magi.Disconnect(c.ctx, magiapi.DisconnectRequest{System: args[0]})
		if err != nil {
			return err
		}
		fmt.Println(r.System, r.State, deref(r.Detail))
		return nil
	case "todos":
		fs := flag.NewFlagSet("todos", flag.ContinueOnError)
		all := fs.Bool("all", false, "also ended to-dos")
		plan := fs.String("plan", "", "only this plan")
		asJSON := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args); err != nil {
			return err
		}
		res, err := c.magi.ListTodos(c.ctx, magiapi.ListTodosRequest{Plan: optional(*plan), IncludeEnded: all})
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(res.Todos)
		}
		for _, t := range res.Todos {
			line := fmt.Sprintf("%s  %-9s %-8s %s", t.Id, t.State, t.Kind, t.Title)
			if t.Plan != nil {
				line += "  [plan " + *t.Plan + "]"
			}
			if t.DueMs != nil {
				line += "  due " + time.UnixMilli(int64(*t.DueMs)).Format("Mon 2 Jan 15:04")
			}
			if t.RemindMs != nil {
				line += "  remind " + time.UnixMilli(int64(*t.RemindMs)).Format("Mon 2 Jan 15:04")
			}
			fmt.Println(line)
		}
	case "todo":
		if len(args) != 2 {
			return errors.New("todo ID done|skip|cancel")
		}
		t, err := c.magi.FinishTodo(c.ctx, magiapi.FinishTodoRequest{Id: args[0], Outcome: args[1]})
		if err != nil {
			return err
		}
		fmt.Printf("%s  %s  %s\n", t.Id, t.State, t.Title)
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
	case "output":
		if len(args) != 2 || (args[1] != "check" && args[1] != "transcript") {
			return errors.New("output ID check|transcript")
		}
		name := map[string]string{"check": "check.output", "transcript": "apprentice.transcript"}[args[1]]
		o, err := c.magi.GetTaskOutput(c.ctx, magiapi.GetTaskOutputRequest{Id: args[0], Name: name})
		if err != nil {
			return err
		}
		if o.Truncated {
			fmt.Fprintln(os.Stderr, "(the start is cut)")
		}
		fmt.Print(o.Text)
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
		repo, title, runner := fs.String("repo", "", "repository"), fs.String("title", "", "title"), fs.String("runner", "", "runner")
		fs.Parse(args)
		t, err := c.magi.StartTask(c.ctx, magiapi.StartTaskRequest{Kind: "code-change", Instructions: strings.Join(fs.Args(), " "),
			Repo: optional(*repo), Title: optional(*title), Runner: optional(*runner)})
		if err != nil {
			return err
		}
		printTask(t)
	case "know":
		past := fs.Bool("past", false, "also facts that are no longer true")
		fs.Parse(args)
		res, err := c.magi.QueryKnowledge(c.ctx, magiapi.QueryKnowledgeRequest{Text: optional(strings.Join(fs.Args(), " ")), IncludeEnded: past})
		if err != nil {
			return err
		}
		printKnowledge(res.Items, res.Records)
	case "remember":
		if len(args) < 2 {
			return errors.New("remember SUBJECT RELATION [OBJECT] [-value V] [-subject-kind K] [-object-kind K] [-summary S] [-also]")
		}
		n := 2
		if len(args) > 2 && !strings.HasPrefix(args[2], "-") {
			n = 3
		}
		value, sk, ok, summary := fs.String("value", "", ""), fs.String("subject-kind", "", ""), fs.String("object-kind", "", ""), fs.String("summary", "", "")
		also := fs.Bool("also", false, "")
		fs.Parse(args[n:])
		req := magiapi.RememberRequest{Subject: args[0], Relation: args[1], Value: optional(*value), SubjectKind: optional(*sk), ObjectKind: optional(*ok),
			Summary: optional(*summary), Also: also}
		if n == 3 {
			req.Object = &args[2]
		}
		r, err := c.magi.Remember(c.ctx, req)
		if err != nil {
			return err
		}
		if r.Note != nil {
			fmt.Println(*r.Note)
		}
		printKnowledge([]magiapi.KnowledgeItem{r.Item}, nil)
	case "fix", "update", "retract":
		usage := cmd + " ID VALUE [-field F] [-m REASON]"
		n := 2
		if cmd == "retract" {
			usage, n = "retract ID [-m REASON] [-erase]", 1
		}
		if len(args) < n {
			return errors.New(usage)
		}
		field, reason, erase := fs.String("field", "", ""), fs.String("m", "", ""), fs.Bool("erase", false, "")
		fs.Parse(args[n:])
		req := magiapi.ChangeKnowledgeRequest{Id: args[0], Operation: cmd, Field: optional(*field), Reason: optional(*reason), Erase: erase}
		if n == 2 {
			req.Value = &args[1]
		}
		r, err := c.magi.ChangeKnowledge(c.ctx, req)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s %s\n", r.Change.Id, r.Change.Operation, r.Change.Record)
		if r.Item != nil {
			printKnowledge([]magiapi.KnowledgeItem{*r.Item}, nil)
		}
	case "merge", "unmerge":
		var r magiapi.ChangeKnowledgeResponse
		var err error
		if cmd == "merge" {
			if len(args) < 2 {
				return errors.New("merge KEEP MERGE [-m REASON]")
			}
			reason := fs.String("m", "", "")
			fs.Parse(args[2:])
			r, err = c.magi.MergeKnowledge(c.ctx, magiapi.MergeKnowledgeRequest{Keep: args[0], Merge: args[1], Reason: optional(*reason)})
		} else {
			if len(args) < 1 {
				return errors.New("unmerge CHANGE")
			}
			r, err = c.magi.UnmergeKnowledge(c.ctx, magiapi.UnmergeKnowledgeRequest{Change: args[0]})
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s %s %s\n", r.Change.Id, r.Change.Operation, r.Change.Record)
		if r.Item != nil {
			printKnowledge([]magiapi.KnowledgeItem{*r.Item}, nil)
		}
	case "history":
		if len(args) < 1 {
			return errors.New("history ID")
		}
		r, err := c.magi.KnowledgeHistory(c.ctx, magiapi.KnowledgeHistoryRequest{Id: args[0]})
		if err != nil {
			return err
		}
		for _, h := range r.Changes {
			fmt.Printf("%s  %-8s %s  %s", time.UnixMilli(int64(h.AtMs)).Format("2006-01-02 15:04"), h.Operation, h.Actor, deref(h.Reason))
			if h.Before != nil {
				fmt.Printf("\n      before %s", *h.Before)
			}
			if h.After != nil {
				fmt.Printf("\n      after  %s", *h.After)
			}
			fmt.Println()
		}
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
	case "words":
		res, err := c.magi.ListLexicon(c.ctx, magiapi.ListLexiconRequest{})
		if err != nil {
			return err
		}
		for _, w := range res.Words {
			say := deref(w.Spoken)
			if w.Ipa != nil {
				say += " /" + *w.Ipa + "/"
			}
			fmt.Printf("%-20s %-30s heard: %s\n", w.Written, strings.TrimSpace(say), strings.Join(w.HeardAs, ", "))
		}
		if !res.CanEdit {
			fmt.Println("(you can read the lexicon; a lexicon editor can change it)")
		}
	case "word":
		say := fs.String("say", "", "text to say instead of the word")
		ipa := fs.String("ipa", "", "misaki phonemes, without slashes")
		heard := fs.String("heard", "", "comma-separated forms that speech to text writes")
		note := fs.String("m", "", "note")
		if len(args) < 1 {
			return errors.New("word WRITTEN [-say S] [-ipa P] [-heard A,B] [-m NOTE]")
		}
		fs.Parse(args[1:])
		var hs []string
		for _, h := range strings.Split(*heard, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hs = append(hs, h)
			}
		}
		w, err := c.magi.PutLexiconWord(c.ctx, magiapi.PutLexiconWordRequest{Written: args[0], Spoken: optional(*say),
			Ipa: optional(strings.Trim(*ipa, "/")), HeardAs: hs, Note: optional(*note)})
		if err != nil {
			return err
		}
		fmt.Printf("%s saved\n", w.Written)
	case "unword":
		if len(args) < 1 {
			return errors.New("unword WRITTEN")
		}
		res, err := c.magi.DeleteLexiconWord(c.ctx, magiapi.DeleteLexiconWordRequest{Written: args[0]})
		if err != nil {
			return err
		}
		if !res.Deleted {
			return fmt.Errorf("%q is not in the lexicon", args[0])
		}
	case "speak":
		voice := fs.String("voice", "", "voice")
		out := fs.String("o", "", "output WAV file")
		if len(args) < 1 {
			return errors.New("speak TEXT [-voice V] [-o FILE]")
		}
		fs.Parse(args[1:])
		res, err := c.magi.Speak(c.ctx, magiapi.SpeakRequest{Text: args[0], Voice: optional(*voice)})
		if err != nil {
			return err
		}
		fmt.Println(res.Spoken)
		if *out != "" {
			return os.WriteFile(*out, res.Audio, 0o644)
		}
	case "egress-reviews":
		state := fs.String("state", "open", "open or decided")
		fs.Parse(args)
		res, err := c.magi.ListEgressReviews(c.ctx, magiapi.ListEgressReviewsRequest{State: state})
		if err != nil {
			return err
		}
		for _, r := range res.Reviews {
			fmt.Printf("%s  %s %s %s  (%d times, last %s, %s)\n  why: %s (%s)\n", r.Id, r.Kind, r.Method, r.Url, r.Count,
				time.UnixMilli(int64(r.LastMs)).Format("2006-01-02 15:04"), r.Owner, r.Reason, r.By)
			if r.Decision != nil {
				fmt.Printf("  decided: %s by %s\n", *r.Decision, deref(r.DecidedBy))
			}
			if len(r.Body) > 0 {
				fmt.Printf("  body: %s\n", trunc(string(r.Body), 400))
			}
		}
		if len(res.Reviews) == 0 {
			fmt.Println("no egress reviews")
		}
	case "egress-review":
		note := fs.String("m", "", "note")
		if len(args) < 2 {
			return errors.New("egress-review ID allow-host|allow-path|allow-layer4|block|dismiss [-m NOTE]")
		}
		fs.Parse(args[2:])
		res, err := c.magi.DecideEgressReview(c.ctx, magiapi.DecideEgressReviewRequest{Id: args[0], Decision: args[1], Note: optional(*note)})
		if err != nil {
			return err
		}
		if res.Rule != nil {
			fmt.Printf("%s: %s (rule: %s)\n", res.Id, res.Decision, *res.Rule)
		} else {
			fmt.Printf("%s: %s\n", res.Id, res.Decision)
		}
	default:
		if ok, err := c.attention(cmd, args); ok {
			return err
		}
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
func (c *cli) waitReply(persona string, sent magiapi.Message) error {
	done := false
	spin := startSpinner("Magi is working")
	c.spin = spin
	defer func() { spin.done(); c.spin = nil }()
	// The activity tells the step ("checking the calendar").
	stopActivity := make(chan struct{})
	defer close(stopActivity)
	if spin.on {
		go func() {
			for {
				select {
				case <-stopActivity:
					return
				case <-time.After(2 * time.Second):
				}
				if a, err := c.magi.Activity(c.ctx, magiapi.ActivityRequest{Persona: optional(persona)}); err == nil && a.Working && a.Text != nil {
					spin.set("Magi is " + *a.Text)
				}
			}
		}()
	}
	return c.read(persona, sent.Seq, true, func(m magiapi.Message) bool {
		for _, l := range m.Links {
			if m.AuthorKind == "persona" && l.Kind == "reply-to" && l.Target.Ref == sent.Id {
				done = true
			}
		}
		return done
	})
}

// read prints messages of the stream with persona after seq. With follow,
// it waits for more until stop returns true.
func (c *cli) read(persona string, after uint64, follow bool, stop func(magiapi.Message) bool) error {
	for {
		res, err := c.magi.Read(c.ctx, magiapi.ReadRequest{Persona: optional(persona), AfterSeq: ptr(after), WaitMs: ptr(uint64(25000))})
		if err != nil {
			if rpc.CodeOf(err) == 0 {
				time.Sleep(2 * time.Second)
				continue
			}
			return err
		}
		for _, m := range res.Messages {
			c.spin.clear(func() { printMessage(m) })
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
// them. It reads through the API, so it sees only the caller's streams.
func (c *cli) usage(since time.Time) error {
	from := uint64(since.UnixMilli())
	personas, err := c.magi.ListPersonas(c.ctx, magiapi.ListPersonasRequest{})
	if err != nil {
		return err
	}
	count, total := map[string]int{}, 0
	for _, p := range personas.Personas {
		for after := uint64(0); ; {
			res, err := c.magi.Read(c.ctx, magiapi.ReadRequest{Persona: &p.Id, AfterSeq: ptr(after), Limit: ptr(uint64(1000))})
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

func (c *cli) chat(persona string) error {
	in := bufio.NewScanner(os.Stdin)
	fmt.Println("Type a message; an empty line or Ctrl-D ends.")
	for {
		fmt.Print("> ")
		if !in.Scan() || strings.TrimSpace(in.Text()) == "" {
			return in.Err()
		}
		text := in.Text()
		res, err := c.magi.Say(c.ctx, magiapi.SayRequest{Persona: optional(persona), Text: &text})
		if err != nil {
			fmt.Fprintln(os.Stderr, "magi:", err)
			continue
		}
		if err := c.waitReply(persona, res.Message); err != nil {
			fmt.Fprintln(os.Stderr, "magi:", err)
		}
	}
}

func printMessage(m magiapi.Message) {
	who := m.Author
	if m.AuthorKind == "persona" {
		who = strings.TrimPrefix(m.Author, "persona:")
	}
	extra := " [" + m.Id
	if m.Visibility == "shared" {
		extra += ", shared"
	}
	extra += "]"
	if m.GeneratedBy != nil {
		extra += " (" + *m.GeneratedBy + ")"
	}
	if m.Audio != nil {
		extra += " [audio " + *m.Audio + "]"
	}
	fmt.Printf("\n%s %s%s:\n%s\n", time.UnixMilli(int64(m.CreatedMs)).Format("15:04:05"), who, extra, m.Text)
	for _, r := range m.Refs {
		if r.Kind == "knowledge-record" {
			continue // the records that the persona read: for Magi, not for people
		}
		fmt.Printf("  -> %s %s %s\n", r.Kind, r.Ref, deref(r.Title))
	}
	for _, l := range m.Links {
		if l.Kind == "reply-to" {
			continue
		}
		fmt.Printf("  %s %s\n", strings.ReplaceAll(l.Kind, "-", " ")+":", firstOf(deref(l.Snippet), deref(l.Target.Title), l.Target.Kind+" "+l.Target.Ref))
	}
}

func printLink(l magiapi.MessageLink) {
	part := ""
	if l.SpanStart != nil {
		part = fmt.Sprintf(" part %d-%d (%.0f%%)", *l.SpanStart, deref(l.SpanEnd), 100*l.Weight)
	}
	fmt.Printf("%s  %-10s %-14s %s %s%s  by %s, confidence %.2f\n", l.Id, l.State, l.Kind, l.Target.Kind, l.Target.Ref, part, l.Author, l.Confidence)
	if s := deref(l.Snippet); s != "" {
		fmt.Printf("      %q\n", s)
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// printKnowledge prints entities with their facts, each fact once.
func printKnowledge(items []magiapi.KnowledgeItem, records []magiapi.KnowledgeRecord) {
	seen := map[string]bool{}
	for _, it := range items {
		fmt.Printf("%s  [%s] %s", it.Id, it.Kind, it.Label)
		if len(it.Aliases) > 0 {
			fmt.Printf(" (%s)", strings.Join(it.Aliases, ", "))
		}
		if it.Summary != "" {
			fmt.Printf(": %s", it.Summary)
		}
		fmt.Println()
		if it.System != nil {
			fmt.Printf("      in %s at %s\n", *it.System, deref(it.Locator))
		}
		for _, f := range it.Facts {
			if seen[f.Id] {
				continue
			}
			seen[f.Id] = true
			other := deref(f.Value)
			if f.Other != nil {
				other = *f.Other
			}
			line := it.Label + " " + f.Relation + " " + other
			if f.Direction == "in" {
				line = other + " " + f.Relation + " " + it.Label
			}
			var notes []string
			if f.Status != "current" {
				notes = append(notes, f.Status)
			}
			if !f.Supported {
				notes = append(notes, "no evidence")
			}
			if f.Stale {
				notes = append(notes, "stale")
			}
			if f.Disputed {
				notes = append(notes, "disputed")
			}
			if !f.Mine {
				notes = append(notes, "not yours")
			}
			if len(notes) > 0 {
				line += "  (" + strings.Join(notes, ", ") + ")"
			}
			fmt.Printf("      %s  %s\n", f.Id, line)
		}
	}
	for _, r := range records {
		fmt.Printf("%s  [%s] %s: %s\n", r.Id, r.Kind, r.Title, r.Summary)
	}
}
