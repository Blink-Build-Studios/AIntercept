package capture

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/canon"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/events"
	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/wsframe"
)

// Options say which call to take and how to describe it.
type Options struct {
	Call  string // its correlation id
	Turns []int  // a WebSocket's turns to keep (none: all)
	Shape string
	Notes string
	// Trim cuts each JSON array in the bodies longer than 2×Trim to its first
	// and last Trim elements (0: none), for a shape that doesn't depend on
	// length (a catalog, a long conversation). Recorded in the case's notes.
	Trim int
	// Models are the model names the corpus's own model calls use
	// (CorpusModels): kept wherever they appear. Any other is a fake.
	Models map[string]bool
}

// rows are one call's rows, in the order its observer wrote them.
type rows struct {
	req, resp            *events.Event
	reqBody, respBody    []byte
	reqChunks, respChunk []int
	complete             *events.Event
	turns                []events.Event
	disconnect           string
	ws                   []wsChunk
	missing              bool // a body chunk without its bytes (content-free capture)
}

type wsChunk struct {
	up   bool
	data []byte
}

// Files lists the raw files under path (a file or a directory), each pod's in
// the order it wrote them.
func Files(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	var out []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ndjson.gz") {
			out = append(out, p)
		}
		return err
	})
	sort.Slice(out, func(i, j int) bool { return filepath.Base(out[i]) < filepath.Base(out[j]) })
	return out, err
}

// eachRow calls f with each row (not record) of the raw files, in order.
func eachRow(files []string, f func(line []byte) error) error {
	for _, p := range files {
		fh, err := os.Open(p)
		if err != nil {
			return err
		}
		zr, err := gzip.NewReader(fh)
		if err != nil {
			fh.Close()
			return fmt.Errorf("%s: %w", p, err)
		}
		sc := bufio.NewScanner(zr)
		sc.Buffer(make([]byte, 1<<20), 256<<20)
		for sc.Scan() {
			l := sc.Bytes()
			if bytes.HasPrefix(l, []byte(`{"record":`)) {
				continue
			}
			if err := f(l); err != nil {
				fh.Close()
				return err
			}
		}
		err = sc.Err()
		fh.Close()
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func collect(files []string, call string) (*rows, error) {
	r := &rows{}
	needle := []byte(`"correlation_id":"` + call + `"`)
	err := eachRow(files, func(l []byte) error {
		if !bytes.Contains(l, needle) {
			return nil
		}
		var e events.Event
		if err := json.Unmarshal(l, &e); err != nil || e.CorrelationID != call {
			return nil
		}
		var body []byte
		if e.BodyB64 != "" {
			b, err := base64.StdEncoding.DecodeString(e.BodyB64)
			if err != nil {
				return fmt.Errorf("a body chunk: %w", err)
			}
			body = b
		} else if e.Bytes > 0 {
			r.missing = true
		}
		ws := r.resp != nil && r.resp.Status == 101
		switch e.Phase {
		case events.PhaseRequestHeaders:
			r.req = &e
		case events.PhaseResponseHeaders:
			r.resp = &e
		case events.PhaseRequestBodyChunk:
			if ws || r.req != nil && strings.EqualFold(r.req.Headers["upgrade"], "websocket") {
				r.ws = append(r.ws, wsChunk{true, body})
			} else {
				r.reqBody = append(r.reqBody, body...)
				r.reqChunks = append(r.reqChunks, len(body))
			}
		case events.PhaseResponseBodyChunk:
			if ws {
				r.ws = append(r.ws, wsChunk{false, body})
			} else {
				r.respBody = append(r.respBody, body...)
				r.respChunk = append(r.respChunk, len(body))
			}
		case events.PhaseComplete:
			if e.Transport == events.TransportWebSocket {
				r.turns = append(r.turns, e)
			} else {
				r.complete = &e
			}
		case events.PhaseWSConnection:
			r.complete = &e
		case events.PhaseDisconnect:
			r.disconnect = e.Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if r.req == nil {
		return nil, fmt.Errorf("no request headers for call %s in these files", call)
	}
	if r.missing {
		return nil, fmt.Errorf("call %s has body chunks without their bytes: capture it in full interception mode", call)
	}
	return r, nil
}

// FromRaw reads one call from the raw files under path and returns it as an
// anonymized case, with the anonymizer that made it (its Who is the real
// identifiers it replaced).
func FromRaw(path string, o Options) (*Case, *Anonymizer, error) {
	files, err := Files(path)
	if err != nil {
		return nil, nil, err
	}
	r, err := collect(files, o.Call)
	if err != nil {
		return nil, nil, err
	}
	a := NewAnonymizer()
	for m := range o.Models {
		a.models[m] = true
	}
	req := r.req
	a.modelCall = modelAPI(req.Host, req.Path)
	c := &Case{}
	c.Request = Request{Method: req.Method, Host: req.Host, Path: a.Path(req.Path), IP: a.ip(req.IP)}
	if req.IP == "" {
		c.Request.IP = ""
	}
	if c.Request.Headers, err = a.Headers(req.Headers); err != nil {
		return nil, nil, err
	}
	c.Request.Credential = credential(a, req, r.complete, r.turns)
	ua := req.Headers["user-agent"]
	cr := canon.Request{Host: req.Host, UserAgent: ua}
	cr.Product = canon.ProductOf(&cr, req.Headers["originator"])
	_, version := canon.ClientOf(&cr, req.Headers["originator"])
	c.Meta = Meta{Provider: providerOf(req.Host), Client: clientOf(cr.Product, ua), API: APIOf(req.Host, req.Path), ClientVersion: version,
		Shape: o.Shape, Captured: time.Unix(0, req.TimestampNS).UTC().Format("2006-01-02"), Notes: o.Notes}
	if o.Trim > 0 {
		c.Meta.Notes = strings.TrimSpace(c.Meta.Notes + fmt.Sprintf(" Arrays longer than %d elements are cut to their first and last %d (capture-fixture -trim %d): the shape doesn't depend on their length.", 2*o.Trim, o.Trim, o.Trim))
	}
	if r.resp != nil {
		c.Response.Status = r.resp.Status
		if c.Response.Headers, err = a.ResponseHeaders(r.resp.Headers); err != nil {
			return nil, nil, err
		}
	} else {
		c.Response.Error = r.disconnect
	}
	if r.resp != nil && r.resp.Status == 101 {
		if c.WS, err = wsMessages(a, r, r.resp.Headers, o.Turns, o.Trim); err != nil {
			return nil, nil, err
		}
		c.Meta.Turns = o.Turns
	} else {
		enc := req.Headers["content-encoding"]
		if c.ReqBody, err = body(a, r.reqBody, enc, req.Headers["content-type"], o.Trim, req.Host, req.Path, 0, false); err != nil {
			return nil, nil, fmt.Errorf("request body: %w", err)
		}
		c.Request.Encoding, c.Request.Chunks = enc, r.reqChunks
		if r.resp != nil {
			enc = r.resp.Headers["content-encoding"]
			if c.RespBody, err = body(a, r.respBody, enc, r.resp.Headers["content-type"], o.Trim, req.Host, req.Path, r.resp.Status, true); err != nil {
				return nil, nil, fmt.Errorf("response body: %w", err)
			}
			c.Response.Encoding, c.Response.Chunks = enc, r.respChunk
		}
	}
	return c, a, nil
}

func body(a *Anonymizer, b []byte, enc, contentType string, trim int, host, path string, status int, resp bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	plain, err := events.DecodeBody(b, enc)
	if err != nil {
		return nil, err
	}
	if trim > 0 && json.Valid(plain) {
		if plain, err = trimArrays(plain, trim); err != nil {
			return nil, err
		}
	}
	return a.BodyOf(plain, contentType, host, path, status, resp)
}

// trimArrays cuts every array longer than 2n elements to its first and last
// n, keeping key order (whitespace isn't kept).
func trimArrays(b []byte, n int) ([]byte, error) {
	var out bytes.Buffer
	if err := trimValue(b, n, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func trimValue(raw []byte, n int, out *bytes.Buffer) error {
	t := bytes.TrimSpace(raw)
	switch {
	case len(t) > 0 && t[0] == '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(t, &elems); err != nil {
			return err
		}
		if len(elems) > 2*n {
			elems = append(elems[:n:n], elems[len(elems)-n:]...)
		}
		out.WriteByte('[')
		for i, e := range elems {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := trimValue(e, n, out); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case len(t) > 0 && t[0] == '{':
		dec := json.NewDecoder(bytes.NewReader(t))
		if _, err := dec.Token(); err != nil {
			return err
		}
		out.WriteByte('{')
		for i := 0; dec.More(); i++ {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return err
			}
			if i > 0 {
				out.WriteByte(',')
			}
			out.Write(quote(k.(string)))
			out.WriteByte(':')
			if err := trimValue(v, n, out); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		out.Write(t)
	}
	return nil
}

func providerOf(host string) string {
	switch {
	case strings.HasSuffix(host, "anthropic.com"), strings.HasSuffix(host, "claude.ai"):
		return "anthropic"
	case strings.HasSuffix(host, "chatgpt.com"), strings.HasSuffix(host, "openai.com"):
		return "openai"
	case strings.HasSuffix(host, "googleapis.com"):
		return "gemini"
	}
	return host
}

// APIOf is the API a call is on, by its host and path: anthropic_messages,
// claude_ai_web, openai_responses, openai_chat_completions, chatgpt_backend,
// gemini; "" for a call outside them (a control-plane endpoint).
func APIOf(host, path string) string {
	p, _, _ := strings.Cut(path, "?")
	switch {
	case host == "api.anthropic.com" && strings.HasPrefix(p, "/v1/messages"):
		return "anthropic_messages"
	case host == "claude.ai":
		return "claude_ai_web"
	case host == "api.openai.com" && strings.HasPrefix(p, "/v1/responses"):
		return "openai_responses"
	case host == "api.openai.com" && strings.HasPrefix(p, "/v1/chat/completions"):
		return "openai_chat_completions"
	case host == "chatgpt.com" && strings.HasPrefix(p, "/backend-api/"):
		return "chatgpt_backend"
	case host == "generativelanguage.googleapis.com":
		return "gemini"
	}
	return ""
}

func clientOf(product, ua string) string {
	switch product {
	case canon.ProductClaudeCode:
		return "claude-code"
	case canon.ProductCodex:
		return "codex"
	}
	if name, _, ok := strings.Cut(ua, "/"); ok && name != "" {
		return strings.ToLower(name)
	}
	return "unknown"
}

// credential is what stands in for the call's redacted credential: its
// header, and its kind by the hint the observer kept (the hint itself is part
// of the credential, so it isn't kept). A ChatGPT token's claims are the ones
// the observer read from it, anonymized.
func credential(a *Anonymizer, req, complete *events.Event, turns []events.Event) *Credential {
	var header string
	for _, h := range []string{"authorization", "x-api-key", "x-goog-api-key", "api-key"} {
		if req.Headers[h] == redacted {
			header = h
			break
		}
	}
	if header == "" {
		return nil
	}
	c := &Credential{Header: header, Kind: CredOpaque}
	if header == "authorization" {
		c.Scheme = "Bearer"
	}
	hint := req.KeyHint
	switch {
	case strings.HasPrefix(hint, "eyJ"):
		c.Kind = CredChatGPT
		auth := authOf(complete)
		for i := 0; auth == nil && i < len(turns); i++ {
			auth = turns[i].Auth
		}
		if auth != nil {
			c.Claims = claims(a, auth)
		}
	case strings.HasPrefix(hint, "sk-ant") && header == "authorization":
		c.Kind = CredAnthropicOAuth
	case strings.HasPrefix(hint, "sk-ant"):
		c.Kind = CredAnthropicKey
	case strings.HasPrefix(hint, "sk-"):
		c.Kind = CredOpenAIKey
	case strings.HasPrefix(hint, "AIza"):
		c.Kind = CredGeminiKey
	}
	return c
}

func authOf(e *events.Event) *events.AuthIdentity {
	if e == nil {
		return nil
	}
	return e.Auth
}

// claims is a ChatGPT token's claims as OpenAI namespaces them (the claim
// keys the code reads, events.AuthIdentityFromHeaders), from what the observer
// read, each value anonymized as it is everywhere else in the case.
func claims(a *Anonymizer, au *events.AuthIdentity) json.RawMessage {
	put := func(m map[string]any, k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	auth := map[string]any{}
	put(auth, "chatgpt_account_id", a.ID(au.AccountID))
	put(auth, "chatgpt_user_id", a.ID(au.UserID))
	put(auth, "chatgpt_plan_type", a.Opaque(au.Plan)) // the account's plan
	profile := map[string]any{}
	if au.Email != "" {
		profile["email"] = a.email(au.Email)
	}
	top := map[string]any{"https://api.openai.com/auth": auth, "https://api.openai.com/profile": profile}
	if au.Name != "" {
		a.Who[strings.ToLower(au.Name)] = true
		top["name"] = a.Default(au.Name)
	}
	if iss := au.Issuer; iss == "https://auth.openai.com" {
		top["iss"] = iss // the provider's own issuer
	} else {
		put(top, "iss", a.Default(iss))
	}
	put(top, "sub", a.Default(au.Subject))
	put(top, "client_id", a.ID(au.ClientID))
	put(top, "session_id", a.ID(au.SessionID))
	if au.ExpiresAt != 0 {
		top["exp"] = au.ExpiresAt
	}
	b, _ := json.Marshal(top)
	return b
}

// wsMessages decodes a WebSocket's frames (both directions, in the order the
// observer saw them) and keeps the given turns' messages: from each kept
// turn's response.create to the next turn's, and a close frame after the
// last.
func wsMessages(a *Anonymizer, r *rows, respHeaders map[string]string, turns []int, trim int) ([]WSMessage, error) {
	ext := wsframe.ParseExtensions(respHeaders["sec-websocket-extensions"])
	up := wsframe.NewDecoder(ext.Deflate, ext.ClientNoContextTakeover, 64<<20)
	down := wsframe.NewDecoder(ext.Deflate, ext.ServerNoContextTakeover, 64<<20)
	type msg struct {
		up   bool
		m    wsframe.Message
		turn int
	}
	var all []msg
	turn := 0
	for _, ch := range r.ws {
		d := down
		if ch.up {
			d = up
		}
		ms, err := d.Feed(ch.data)
		if err != nil {
			return nil, fmt.Errorf("websocket frames: %w", err)
		}
		for _, m := range ms {
			if m.Opcode != wsframe.OpText && m.Opcode != wsframe.OpBinary && m.Opcode != wsframe.OpClose {
				continue
			}
			if ch.up && m.Opcode == wsframe.OpText && wsType(m.Payload) == "response.create" {
				turn++
			}
			all = append(all, msg{ch.up, m, turn})
		}
	}
	keep := func(t int) bool {
		if len(turns) == 0 {
			return true
		}
		for _, k := range turns {
			if k == t {
				return true
			}
		}
		return false
	}
	last := 0
	for _, t := range turns {
		last = max(last, t)
	}
	var kept []msg
	for _, m := range all {
		if len(turns) == 0 || keep(m.turn) && m.turn > 0 || m.m.Opcode == wsframe.OpClose && m.turn == last {
			kept = append(kept, m)
		}
	}
	// The server's messages are a reply's events: anonymized together, so
	// streamed pieces still add up.
	var evs [][]byte
	var at []int
	out := make([]WSMessage, len(kept))
	for i, m := range kept {
		if trim > 0 && json.Valid(m.m.Payload) {
			p, err := trimArrays(m.m.Payload, trim)
			if err != nil {
				return nil, err
			}
			kept[i].m.Payload, m.m.Payload = p, p
		}
		out[i].Dir = "down"
		if m.up {
			out[i].Dir = "up"
		}
		switch {
		case m.m.Opcode == wsframe.OpClose:
			code := m.m.CloseCode
			out[i].Close = &code
		case m.m.Opcode == wsframe.OpBinary:
			out[i].Binary = base64.StdEncoding.EncodeToString(make([]byte, len(m.m.Payload)))
		case !json.Valid(m.m.Payload):
			out[i].Text = a.Text(string(m.m.Payload))
		case !a.modelCall: // nothing reads a control-plane socket's messages: their structure only
			b, err := a.Unread(m.m.Payload, nil)
			if err != nil {
				return nil, err
			}
			out[i].Msg = b
		case m.up:
			b, err := a.JSON(m.m.Payload)
			if err != nil {
				return nil, err
			}
			out[i].Msg = b
		default:
			evs = append(evs, m.m.Payload)
			at = append(at, i)
		}
	}
	anon, err := a.Events(evs)
	if err != nil {
		return nil, err
	}
	for j, i := range at {
		out[i].Msg = anon[j]
	}
	return out, nil
}

func wsType(p []byte) string {
	var h struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(p, &h)
	return h.Type
}

// CheckDir checks every file of a written case (Anonymizer.Check).
func CheckDir(dir string, a *Anonymizer) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return a.Check(filepath.Base(p), b)
	})
}

// List writes one line per call in the raw files: its id, host, method,
// path, status, user agent and body sizes, and a WebSocket's turns with
// their models: what to choose a case from. The paths are as captured, so
// the list stays local.
func List(path string, w io.Writer) error {
	files, err := Files(path)
	if err != nil {
		return err
	}
	type info struct {
		e                   *events.Event
		ua                  string
		reqBytes, respBytes int
		turns               []string
	}
	calls := map[string]*info{}
	return eachRow(files, func(l []byte) error {
		var e events.Event
		if json.Unmarshal(l, &e) != nil || e.CorrelationID == "" || e.Synthetic {
			return nil
		}
		c := calls[e.CorrelationID]
		if c == nil {
			c = &info{}
			calls[e.CorrelationID] = c
		}
		switch e.Phase {
		case events.PhaseRequestHeaders:
			c.e, c.ua = &e, e.Headers["user-agent"]
		case events.PhaseRequestBodyChunk:
			c.reqBytes += e.Bytes
		case events.PhaseResponseBodyChunk:
			c.respBytes += e.Bytes
		case events.PhaseComplete, events.PhaseWSConnection:
			if e.Transport == events.TransportWebSocket && e.Phase == events.PhaseComplete {
				c.turns = append(c.turns, fmt.Sprintf("%d:%s:%s", e.WSTurn, e.Model, e.StopReason))
				return nil
			}
			delete(calls, e.CorrelationID)
			if c.e == nil {
				return nil
			}
			_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\treq=%d\tresp=%d\t%s\n", e.CorrelationID, c.e.Host, c.e.Method,
				c.e.Path, e.Status, c.ua, c.reqBytes, c.respBytes, strings.Join(c.turns, ","))
			return err
		}
		return nil
	})
}

// CorpusModels is every model name the corpus's model calls under root ask
// for: the ones a case keeps (Options.Models).
func CorpusModels(root string) (map[string]bool, error) {
	dirs, err := Cases(root)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	note := func(b []byte) {
		var v struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(b, &v) == nil && reModel.MatchString(v.Model) {
			out[v.Model] = true
		}
	}
	for _, d := range dirs {
		c, err := Load(d)
		if err != nil {
			return nil, err
		}
		if !modelAPI(c.Request.Host, c.Request.Path) {
			continue
		}
		note(c.ReqBody)
		for _, m := range c.WS {
			if m.Dir == "up" {
				note(m.Msg)
			}
		}
	}
	return out, nil
}

// HighEntropy is whether an identifier is random enough (about 100 bits or
// more: a UUID, a long random device or session id) that its hash can't be
// reversed by guessing. Only those go in the committed denylist; an email,
// an IP, a name, a host or a short or structured id would be found from its
// hash by trying the likely values, so its hash stays local.
func HighEntropy(s string) bool {
	if reUUID.FindString(s) == s && len(s) == 36 {
		return true
	}
	if i := strings.LastIndexAny(s, "-_"); i > 0 && i <= 8 && strings.ToLower(s[:i]) == s[:i] && isWord(s[:i]) {
		s = s[i+1:] // a type prefix (user-, org-, acct_) is no randomness
	}
	var lower, upper, digit, hexOnly = false, false, false, true
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= '0' && c <= '9':
			digit = true
		default:
			return false
		}
		if !isHex(c) {
			hexOnly = false
		}
	}
	if !randomish(s) {
		return false
	}
	bits := 0.0
	switch {
	case hexOnly && !(lower && upper):
		bits = 4
	case lower && upper && digit:
		bits = 5.95
	case lower && upper:
		bits = 5.7
	case digit && !lower && !upper:
		bits = 3.32
	default:
		bits = 5.17
	}
	return bits*float64(len(s)) >= 100
}
