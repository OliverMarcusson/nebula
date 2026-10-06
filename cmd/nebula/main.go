package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/olivermarcusson/nebula/internal/accounts"
	"github.com/olivermarcusson/nebula/internal/logins"
	"github.com/olivermarcusson/nebula/internal/oidc"
	"github.com/olivermarcusson/nebula/internal/sessions"
	"github.com/olivermarcusson/nebula/web"
)

// version is set at release build time.
var version = "dev"

func main() {
	loadConfig()
	// Installed as `claude` or `nebula-claude`, the binary is the launcher.
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if name == "claude" || name == "nebula-claude" {
		os.Exit(launch(os.Args[1:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "claude" {
		os.Exit(launch(os.Args[2:]))
	}
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func projectsPath() string {
	if p := os.Getenv("CLAUDE_CONFIG_DIR"); p != "" {
		return filepath.Join(p, "projects")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "projects")
}
func flags(name string) *flag.FlagSet { return flag.NewFlagSet(name, flag.ContinueOnError) }
func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: nebula setup|init|serve|sync|login|accounts|claude|limited|list|fetch|restore")
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "init":
		return initConfig(args)
	case "serve":
		return serve(args)
	case "sync":
		return syncSessions(args)
	case "accounts":
		return reportAccounts(args)
	case "login":
		return login(args)
	case "limited":
		return limitedCommand(args)
	case "setup":
		return setup(args)
	case "version", "--version":
		fmt.Println("nebula", version)
		return nil
	case "list":
		f := flags("list")
		if err := parse(f, args); err != nil {
			return err
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		var list []sessions.Snapshot
		if err = c.call(context.Background(), "GET", "/v1/sessions", nil, &list); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(list)
	case "fetch", "restore":
		f := flags(os.Args[1])
		device := f.String("device", "", "source device UUID")
		id := f.String("session", "", "native session UUID")
		revision := f.String("revision", "", "revision digest (default newest)")
		projects := f.String("projects", projectsPath(), "destination native projects directory")
		if err := parse(f, args); err != nil {
			return err
		}
		if !sessions.ID(*device) || !sessions.ID(*id) || (*revision != "" && !sessions.Digest(*revision)) {
			return errors.New("valid device/session UUIDs and revision required")
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		p := "/v1/sessions/" + *device + "/" + *id
		if *revision != "" {
			p += "?revision=" + *revision
		}
		var rec sessions.Record
		if err = c.call(context.Background(), "GET", p, nil, &rec); err != nil {
			return err
		}
		if err = rec.Validate(); err != nil {
			return err
		}
		if rec.Bundle.DeviceID != *device || rec.Bundle.SessionID != *id || (*revision != "" && rec.Snapshot.Revision != *revision) {
			return errors.New("server returned a different session")
		}
		if os.Args[1] == "fetch" {
			return json.NewEncoder(os.Stdout).Encode(rec)
		}
		if err = sessions.Restore(*projects, rec); err != nil {
			return err
		}
		fmt.Println("Session restored; open its original workspace in Claude Code to resume.")
		return nil
	default:
		return errors.New("unknown command")
	}
}
func initConfig(args []string) error {
	f := flags("init")
	owner := f.String("user", "", "Nebula archive owner")
	dir := f.String("dir", ".nebula", "new credential directory")
	if err := parse(f, args); err != nil {
		return err
	}
	if strings.TrimSpace(*owner) == "" {
		return errors.New("--user is required")
	}
	if err := os.Mkdir(*dir, 0700); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	data, err := json.Marshal(map[string]string{*owner: token})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*dir, "users.json"), data, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(*dir, "device.token"), []byte(token+"\n"), 0600); err != nil {
		return err
	}
	fmt.Println("Created users.json for the server and device.token for the companion in", *dir)
	return nil
}
func serve(args []string) error {
	f := flags("serve")
	listen := f.String("listen", "127.0.0.1:13003", "HTTP bind address")
	data := f.String("data", ".nebula/data", "persistent archive directory")
	auth := f.String("auth", env("NEBULA_AUTH_FILE", ".nebula/users.json"), "JSON archive owner to bearer token map")
	publicURL := f.String("public-url", os.Getenv("NEBULA_PUBLIC_URL"), "public https origin, needed for dashboard sign-in")
	issuer := f.String("oidc-issuer", os.Getenv("NEBULA_OIDC_ISSUER"), "OpenID Connect issuer for dashboard sign-in (Claustra)")
	clientID := f.String("oidc-client-id", os.Getenv("NEBULA_OIDC_CLIENT_ID"), "OpenID Connect client id; the secret is read from NEBULA_OIDC_CLIENT_SECRET")
	oidcOwner := f.String("oidc-owner", os.Getenv("NEBULA_OIDC_OWNER"), "archive owner dashboard sign-ins act as (default: the only owner)")
	oidcEmails := f.String("oidc-emails", os.Getenv("NEBULA_OIDC_EMAILS"), "comma-separated verified emails allowed to sign in, checked in addition to the provider")
	vaultDir := f.String("vault", os.Getenv("NEBULA_VAULT_DIR"), "directory for Claude sign-ins shared with every device (needs the claude CLI)")
	if err := parse(f, args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*auth)
	if err != nil {
		return err
	}
	var users map[string]string
	if err = json.Unmarshal(raw, &users); err != nil {
		return errors.New("invalid auth file")
	}
	if len(users) == 0 {
		return errors.New("auth file has no users")
	}
	type credential struct {
		owner string
		hash  [32]byte
	}
	credentials := []credential{}
	seen := map[string]bool{}
	for owner, token := range users {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if owner == "" || err != nil || len(decoded) < 32 || seen[token] {
			return errors.New("auth file contains invalid or duplicate credentials")
		}
		seen[token] = true
		credentials = append(credentials, credential{owner, sha256.Sum256([]byte(token))})
	}
	var signin *oidc.Provider
	if *issuer != "" {
		owner := *oidcOwner
		if owner == "" && len(credentials) == 1 {
			owner = credentials[0].owner
		}
		key, err := sessionKey(filepath.Join(filepath.Dir(filepath.Clean(*data)), "session.key"))
		if err != nil {
			return err
		}
		emails := []string{}
		for _, e := range strings.Split(*oidcEmails, ",") {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				emails = append(emails, e)
			}
		}
		signin, err = oidc.New(oidc.Config{Issuer: *issuer, ClientID: *clientID, ClientSecret: os.Getenv("NEBULA_OIDC_CLIENT_SECRET"), PublicURL: *publicURL, Owner: owner, Emails: emails, Key: key})
		if err != nil {
			return err
		}
		log.Print("Dashboard sign-in through ", *issuer, " as owner ", owner)
	}
	store, err := sessions.Open(*data)
	if err != nil {
		return err
	}
	defer store.Close()
	accountStore, err := accounts.Open(filepath.Join(*data, "accounts"))
	if err != nil {
		return err
	}
	defer accountStore.Close()
	signIns := logins.New()
	var shared *vault
	if *vaultDir != "" {
		claude, err := realClaude()
		if err != nil {
			return err
		}
		owners := []string{}
		for _, c := range credentials {
			owners = append(owners, c.owner)
		}
		if err = os.MkdirAll(*vaultDir, 0700); err != nil {
			return err
		}
		shared = &vault{dir: filepath.Clean(*vaultDir), claude: claude, owners: owners, signIns: signIns, store: accountStore}
		log.Print("Sharing Claude sign-ins with every device, using ", claude)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		switch {
		case r.URL.Path == "/auth/config":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"claustra": signin != nil})
		case signin == nil:
			http.NotFound(w, r)
		case r.URL.Path == "/auth/login" && r.Method == "GET":
			signin.Start(w, r)
		case r.URL.Path == "/auth/callback" && r.Method == "GET":
			if _, err := signin.Callback(w, r); err != nil {
				log.Print("Dashboard sign-in failed: ", err)
			}
		case r.URL.Path == "/auth/logout" && r.Method == "POST":
			if r.Header.Get("Origin") != signin.Origin() {
				http.Error(w, "cross-origin request refused", 403)
				return
			}
			signin.Logout(w)
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		owner := ""
		if bearer {
			hash := sha256.Sum256([]byte(token))
			for _, c := range credentials {
				if subtle.ConstantTimeCompare(hash[:], c.hash[:]) == 1 {
					owner = c.owner
				}
			}
		} else if signin != nil {
			// Dashboard session. Its cookie is SameSite=Strict; writes must also
			// come from the dashboard's own origin.
			if o, ok := signin.Session(r); ok {
				if r.Method == "GET" || r.Method == "HEAD" || r.Header.Get("Origin") == signin.Origin() {
					owner = o
				} else {
					http.Error(w, "cross-origin request refused", 403)
					return
				}
			}
		}
		if owner == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", 401)
			return
		}
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if r.URL.Path == "/v1/me" {
			write(map[string]string{"owner": owner})
			return
		}
		if r.URL.Path == "/v1/vault" {
			// Access tokens go to device companions only, never to a browser.
			switch {
			case !bearer:
				http.Error(w, "device token required", 403)
			case r.Method != "GET":
				http.Error(w, "method not allowed", 405)
			case shared == nil:
				write(map[string]any{"accounts": []vaultToken{}})
			default:
				write(map[string]any{"accounts": shared.Tokens(owner)})
			}
			return
		}
		if loginsPath(r.URL.Path) {
			loginsAPI(w, r, owner, signIns, accountStore, write)
			return
		}
		if r.URL.Path == "/v1/accounts" || strings.HasPrefix(r.URL.Path, "/v1/accounts/") || strings.HasPrefix(r.URL.Path, "/v1/devices/") {
			accountsAPI(w, r, owner, accountStore, shared, write)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/chunks") || strings.HasPrefix(r.URL.Path, "/v1/sessions") {
			archiveAPI(w, r, owner, store, write)
			return
		}
		http.NotFound(w, r)
	})
	mux.Handle("/", dashboard())
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(shutdown)
	}()
	if shared != nil {
		go shared.run(ctx)
	}
	log.Print("Nebula archive listening on ", *listen)
	err = server.ListenAndServe()
	stop()
	// ListenAndServe returns when shutdown starts; keep storage open until it finishes.
	if shutdownErr := <-shutdownDone; shutdownErr != nil {
		_ = server.Close()
		return shutdownErr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// sessionKey loads the dashboard cookie key, creating it on first start.
// Deleting the file signs every browser out.
func sessionKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		return nil, err
	}
	return b, nil
}

// decodeJSON reads one bounded JSON object with no unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		http.Error(w, "invalid request body", 400)
		return false
	}
	return true
}

// accountsAPI serves account management. Devices report the accounts their
// native profiles are signed into; the owner connects, orders and enables them.
func accountsAPI(w http.ResponseWriter, r *http.Request, owner string, store *accounts.Store, shared *vault, write func(any)) {
	respond := func(list []accounts.Account, err error) {
		switch {
		case errors.Is(err, accounts.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, accounts.ErrInvalid):
			http.Error(w, err.Error(), 400)
		case err != nil:
			http.Error(w, "account store unavailable", 500)
		default:
			write(list)
		}
	}
	method := func(m string) bool {
		if r.Method != m {
			w.Header().Set("Allow", m)
			http.Error(w, "method not allowed", 405)
			return false
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	switch {
	case len(parts) == 1: // /v1/accounts
		if method("GET") {
			respond(store.List(owner))
		}
	case parts[0] == "devices" && len(parts) == 3 && parts[2] == "accounts" && sessions.ID(parts[1]):
		var rep accounts.Report
		if method("PUT") && decodeJSON(w, r, &rep) {
			respond(store.Report(owner, parts[1], rep))
		}
	case parts[0] == "accounts" && len(parts) == 2 && parts[1] == "order":
		var body struct {
			IDs []string `json:"ids"`
		}
		if method("PUT") && decodeJSON(w, r, &body) {
			respond(store.Reorder(owner, body.IDs))
		}
	case parts[0] == "accounts" && len(parts) == 3 && sessions.ID(parts[1]) && parts[2] == "limited":
		var body struct {
			ResetsAt *time.Time `json:"resets_at"`
		}
		if method("POST") && decodeJSON(w, r, &body) {
			respond(store.SetLimited(owner, parts[1], body.ResetsAt))
		}
	case parts[0] == "accounts" && len(parts) == 3 && sessions.ID(parts[1]) && (parts[2] == "connect" || parts[2] == "disconnect"):
		if method("POST") {
			if parts[2] == "connect" {
				respond(store.Connect(owner, parts[1]))
				return
			}
			if shared != nil {
				// Disconnecting a shared account signs the server out of it too.
				if err := shared.Remove(owner, parts[1]); err != nil {
					log.Print("Could not remove shared sign-in: ", err)
				}
			}
			respond(store.Disconnect(owner, parts[1]))
		}
	case parts[0] == "accounts" && len(parts) == 2 && sessions.ID(parts[1]):
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if method("PATCH") && decodeJSON(w, r, &body) {
			if body.Enabled == nil {
				http.Error(w, "enabled is required", 400)
				return
			}
			respond(store.SetEnabled(owner, parts[1], *body.Enabled))
		}
	default:
		http.NotFound(w, r)
	}
}

// archiveAPI serves the session archive. Uploads send only chunks the server
// lacks, then commit a manifest; the server reassembles and validates it.
func archiveAPI(w http.ResponseWriter, r *http.Request, owner string, store *sessions.Store, write func(any)) {
	fail := func(err error) {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			http.NotFound(w, r)
		case errors.Is(err, sessions.ErrMissingChunk):
			http.Error(w, err.Error(), 409)
		case errors.Is(err, sessions.ErrInvalid):
			http.Error(w, err.Error(), 400)
		default:
			log.Print("Archive error: ", err)
			http.Error(w, "archive unavailable", 500)
		}
	}
	method := func(m string) bool {
		if r.Method != m {
			w.Header().Set("Allow", m)
			http.Error(w, "method not allowed", 405)
			return false
		}
		return true
	}
	p := r.URL.Path
	switch {
	case p == "/v1/sessions":
		if method("GET") {
			list, err := store.List(owner, r.URL.Query().Get("latest") == "1")
			if err != nil {
				fail(err)
				return
			}
			write(list)
		}
	case p == "/v1/chunks/missing":
		var body struct {
			IDs []string `json:"ids"`
		}
		if method("POST") && decodeJSON(w, r, &body) {
			if len(body.IDs) > 4096 {
				http.Error(w, "too many chunk ids", 400)
				return
			}
			missing, err := store.Missing(owner, body.IDs)
			if err != nil {
				fail(err)
				return
			}
			write(map[string][]string{"missing": missing})
		}
	case strings.HasPrefix(p, "/v1/chunks/"):
		id := strings.TrimPrefix(p, "/v1/chunks/")
		if !sessions.Digest(id) {
			http.NotFound(w, r)
			return
		}
		if !method("PUT") {
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sessions.ChunkMax))
		if err != nil {
			http.Error(w, "chunk too large", 413)
			return
		}
		if err = store.PutChunk(owner, id, data); err != nil {
			fail(err)
			return
		}
		write(map[string]string{"id": id})
	default:
		parts := strings.Split(strings.TrimPrefix(p, "/v1/sessions/"), "/")
		if !strings.HasPrefix(p, "/v1/sessions/") || len(parts) != 2 || !sessions.ID(parts[0]) || !sessions.ID(parts[1]) {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			rec, err := store.Get(owner, parts[0], parts[1], r.URL.Query().Get("revision"))
			if err != nil {
				fail(err)
				return
			}
			write(rec)
		case "PUT":
			r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			var m sessions.Manifest
			if err := dec.Decode(&m); err != nil || dec.More() {
				http.Error(w, "invalid manifest", 400)
				return
			}
			if m.DeviceID != parts[0] || m.SessionID != parts[1] {
				http.Error(w, "session identity mismatch", 400)
				return
			}
			snap, err := store.Commit(owner, m)
			if err != nil {
				fail(err)
				return
			}
			write(snap)
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", 405)
		}
	}
}

func loginsPath(p string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/v1/"), "/")
	return p == "/v1/devices" || parts[0] == "logins" ||
		(parts[0] == "devices" && len(parts) >= 3 && (parts[2] == "poll" || parts[2] == "logins"))
}

// loginsAPI relays dashboard-initiated Claude sign-ins to the chosen device's
// companion, which runs Claude Code's own login for a new profile.
func loginsAPI(w http.ResponseWriter, r *http.Request, owner string, m *logins.Manager, store *accounts.Store, write func(any)) {
	respond := func(v any, err error) {
		switch {
		case errors.Is(err, logins.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, logins.ErrOffline):
			http.Error(w, "device is offline; run nebula sync --watch on it", 409)
		case errors.Is(err, logins.ErrInvalid):
			http.Error(w, err.Error(), 400)
		case err != nil:
			http.Error(w, "sign-in unavailable", 500)
		default:
			write(v)
		}
	}
	method := func(m string) bool {
		if r.Method != m {
			w.Header().Set("Allow", m)
			http.Error(w, "method not allowed", 405)
			return false
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "devices":
		if method("GET") {
			write(m.Devices(owner))
		}
	case parts[0] == "devices" && len(parts) == 3 && parts[2] == "poll" && sessions.ID(parts[1]):
		var body struct {
			Name string `json:"name"`
		}
		if method("POST") && decodeJSON(w, r, &body) {
			write(m.Poll(owner, parts[1], body.Name))
		}
	case parts[0] == "devices" && len(parts) == 4 && parts[2] == "logins" && sessions.ID(parts[1]):
		var body struct {
			State     string `json:"state"`
			URL       string `json:"url"`
			Message   string `json:"message"`
			AccountID string `json:"account_id"`
		}
		if method("PUT") && decodeJSON(w, r, &body) {
			l, err := m.Update(owner, parts[1], parts[3], body.State, body.URL, body.Message, body.AccountID)
			if err == nil && l.State == logins.Completed && body.State == logins.Completed {
				// The device reported its profiles first; signing in is consent to connect.
				if _, cerr := store.Connect(owner, l.AccountID); cerr != nil {
					log.Print("Could not connect signed-in account: ", cerr)
				}
			}
			respond(l, err)
		}
	case len(parts) == 1: // /v1/logins
		var body struct {
			DeviceID string `json:"device_id"`
		}
		if method("POST") && decodeJSON(w, r, &body) {
			respond(m.Create(owner, body.DeviceID))
		}
	case len(parts) == 2:
		if method("GET") {
			respond(m.Get(owner, parts[1]))
		}
	case len(parts) == 3 && parts[2] == "code":
		var body struct {
			Code string `json:"code"`
		}
		if method("POST") && decodeJSON(w, r, &body) {
			respond(m.SubmitCode(owner, parts[1], strings.TrimSpace(body.Code)))
		}
	case len(parts) == 3 && parts[2] == "cancel":
		if method("POST") {
			respond(m.Cancel(owner, parts[1]))
		}
	default:
		http.NotFound(w, r)
	}
}

// dashboard serves the embedded web UI. The UI authenticates API calls with the
// same bearer token as the companion; static assets themselves are public.
func dashboard() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", 405)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "dashboard not built; run npm run build in web/ and rebuild nebula", 404)
			return
		}
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		// Unknown paths fall back to the single-page app.
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

type client struct {
	base, token string
	http        *http.Client
}

func newClient() (*client, error) {
	base := strings.TrimRight(env("NEBULA_SERVER_URL", "http://127.0.0.1:13003"), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid NEBULA_SERVER_URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("Nebula requires HTTPS outside loopback")
	}
	token, err := os.ReadFile(env("NEBULA_TOKEN_FILE", ".nebula/device.token"))
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(token))
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) < 32 {
		return nil, errors.New("invalid device token file")
	}
	return &client{base: base, token: value, http: &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *client) call(ctx context.Context, method, path string, body, out any) error {
	var data []byte
	var err error
	raw, isRaw := body.([]byte)
	if isRaw {
		data = raw
	} else if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if isRaw {
		req.Header.Set("Content-Type", "application/octet-stream")
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("Nebula request failed; check the server connection")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		if text := strings.TrimSpace(string(msg)); text != "" && res.StatusCode < 500 {
			return fmt.Errorf("Nebula returned HTTP %d: %s", res.StatusCode, text)
		}
		return fmt.Errorf("Nebula returned HTTP %d", res.StatusCode)
	}
	resp, err := io.ReadAll(io.LimitReader(res.Body, sessions.MaxBytes+4097))
	if err != nil {
		return err
	}
	if len(resp) > sessions.MaxBytes+4096 {
		return errors.New("server response is too large")
	}
	return json.Unmarshal(resp, out)
}
func deviceID() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "Nebula")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "device-id")
	if data, err := os.ReadFile(p); err == nil {
		id := strings.TrimSpace(string(data))
		if !sessions.ID(id) {
			return "", errors.New("invalid stored device UUID")
		}
		return id, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
	temp, err := os.CreateTemp(dir, ".device-")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	_, err = temp.WriteString(id + "\n")
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Link(temp.Name(), p); errors.Is(err, fs.ErrExist) {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		id = strings.TrimSpace(string(data))
		if !sessions.ID(id) {
			return "", errors.New("invalid stored device UUID")
		}
	} else if err != nil {
		return "", err
	}
	return id, nil
}

// upload sends the chunks the server lacks, then commits the manifest.
func upload(ctx context.Context, c *client, b sessions.Bundle, revision string) error {
	m, chunks := b.Split()
	ids := make([]string, 0, len(chunks))
	for id := range chunks {
		ids = append(ids, id)
	}
	for len(ids) > 0 {
		batch := ids[:min(len(ids), 4096)]
		ids = ids[len(batch):]
		var res struct {
			Missing []string `json:"missing"`
		}
		if err := c.call(ctx, "POST", "/v1/chunks/missing", map[string][]string{"ids": batch}, &res); err != nil {
			return err
		}
		for _, id := range res.Missing {
			data, ok := chunks[id]
			if !ok {
				return errors.New("server asked for an unknown chunk")
			}
			var out map[string]string
			if err := c.call(ctx, "PUT", "/v1/chunks/"+id, data, &out); err != nil {
				return err
			}
		}
	}
	var snap sessions.Snapshot
	if err := c.call(ctx, "PUT", "/v1/sessions/"+b.DeviceID+"/"+b.SessionID, m, &snap); err != nil {
		return err
	}
	if snap.Revision != revision || snap.DeviceID != b.DeviceID || snap.SessionID != b.SessionID {
		return errors.New("upload confirmation mismatch")
	}
	return nil
}

func syncSessions(args []string) error {
	f := flags("sync")
	projects := f.String("projects", projectsPath(), "native Claude Code projects directory")
	device := f.String("device", "", "stable device UUID")
	watch := f.Bool("watch", false, "periodically synchronize")
	interval := f.Duration("interval", 10*time.Second, "watch polling interval")
	profiles := f.String("profiles", os.Getenv("NEBULA_PROFILES"), "Claude Code config directories whose accounts to report")
	if err := parse(f, args); err != nil {
		return err
	}
	if *interval < time.Second {
		return errors.New("interval must be at least one second")
	}
	if *device == "" {
		id, err := deviceID()
		if err != nil {
			return err
		}
		*device = id
	}
	if !sessions.ID(*device) {
		return errors.New("invalid device UUID")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var lastReport []byte
	var reportedAt time.Time
	// Account reports are best effort and only sent when changed or every few minutes.
	reportOnce := func() {
		rep, err := localAccounts(*profiles)
		if err != nil {
			log.Print("Account report skipped: ", err)
			return
		}
		data, _ := json.Marshal(rep)
		if bytes.Equal(data, lastReport) && time.Since(reportedAt) < 5*time.Minute {
			return
		}
		if err := sendReport(ctx, c, *device, rep); err != nil {
			log.Print("Account report failed: ", err)
			return
		}
		lastReport, reportedAt = data, time.Now()
	}
	if *watch {
		// The watcher also runs dashboard-initiated sign-ins on this device.
		go (&loginAgent{c: c, device: *device, profiles: *profiles}).run(ctx)
	}
	// confirmed remembers each session's file signature once the server holds
	// that content, so the watcher skips unchanged sessions without reading them.
	confirmed := map[string]string{}
	reported := map[string]string{}
	var sharedAt time.Time
	syncOnce := func() error {
		// Shared accounts first, so the report below includes new ones.
		if time.Since(sharedAt) >= time.Minute {
			sharedAt = time.Now()
			if err := syncShared(ctx, c, *profiles); err != nil {
				log.Print("Shared accounts not updated: ", err)
			}
		}
		reportOnce()
		var list []sessions.Snapshot
		if err := c.call(ctx, "GET", "/v1/sessions?latest=1", nil, &list); err != nil {
			return err
		}
		latest := map[string]string{}
		for _, m := range list {
			latest[m.DeviceID+"/"+m.SessionID] = m.Revision
		}
		uploaded := 0
		dirs, err := projectDirs(*projects, *profiles)
		for _, dir := range dirs {
			if err != nil {
				break
			}
			err = sessions.Scan(dir, *device, sessions.ScanOptions{
				Skip: func(id, sig string) bool { return confirmed[dir+"/"+id] == sig },
				Invalid: func(id string, err error) {
					if reported[dir+"/"+id] != err.Error() {
						reported[dir+"/"+id] = err.Error()
						log.Printf("Skipping session %s: %v", id, err)
					}
				},
				Visit: func(b sessions.Bundle, sig string) error {
					revision := b.Revision()
					if latest[b.DeviceID+"/"+b.SessionID] != revision {
						if err := upload(ctx, c, b, revision); err != nil {
							return err
						}
						uploaded++
					}
					confirmed[dir+"/"+b.SessionID] = sig
					return nil
				},
			})
		}
		if err == nil {
			fmt.Printf("Synced %d new session revision(s).\n", uploaded)
		}
		return err
	}
	for {
		err := syncOnce()
		if !*watch {
			return err
		}
		if err != nil && ctx.Err() == nil {
			log.Print("Sync failed; local sessions retained; retrying: ", err)
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
