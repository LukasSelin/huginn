package story

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// chapter is the facts of a small chapter.
func chapter() Facts {
	t := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	return Facts{
		Number: 2, Of: 3, From: t, To: t.AddDate(0, 0, 2), FirstRev: 4, LastRev: 9, Commits: 6,
		Added: 14, Removed: 3, Signature: 2, Edits: 25,
		Hottest: "internal/git", HottestEdits: 18, Runner: "internal/ast", RunnerEdits: 7,
		Births: []string{"internal/git"}, Live: 40, LivePackages: 3,
		Moments: []Moment{
			{Kind: Birth, Time: t, Rev: 4, SHA: "abc1234", Package: "internal/git", Count: 9, Text: "internal/git appears with 9 symbols", Weight: 5.3},
			{Kind: Signature, Time: t.AddDate(0, 0, 1), Rev: 6, SHA: "def5678", Package: "internal/git", Count: 2, Text: "2 exported signatures changed, including (*Repo).Walk", Weight: 2.1},
		},
		Previous: []string{"Beginnings: cmd/huginn"},
	}
}

func nar(title, text string) Narration { return Narration{Title: title, Text: text} }

func TestValidate(t *testing.T) {
	f := chapter()
	base, _ := Templates{}.Narrate(context.Background(), f)
	good := Narration{
		Title: "Enter internal/git",
		Text: "Over 6 commits between October 12 and 14, 2026, internal/git arrived with 9 symbols and drew 18 of the 25 semantic edits. " +
			"Two exported signatures changed, among them (*Repo).Walk. The chapter ends with 40 live symbols in 3 packages.",
	}
	if v, err := validate(f, base, good); err != nil || len(v) > 0 {
		t.Fatalf("good narration rejected: %v %v", v, err)
	}

	for _, tc := range []struct {
		name string
		n    Narration
		want string
	}{
		{"invented number", nar("Enter internal/git", "Six commits landed. The package grew by 120 symbols."), "number 120"},
		{"invented number word", nar("Enter internal/git", "Six commits landed. Eleven packages changed."), `"Eleven"`},
		{"invented package", nar("Enter internal/git", "Six commits landed. Most edits went to internal/web."), `"internal/web"`},
		{"invented symbol", nar("Enter internal/git", "Six commits landed. The work centered on (*Repo).Diff."), `"(*Repo).Diff"`},
		{"invented person", nar("Enter internal/git", "Six commits landed. Most of them came from Ada Lovelace."), `"Ada"`},
		{"long title", nar("The long road into the git package begins", good.Text), "title has 8 words"},
		{"one sentence", nar("Enter internal/git", "Six commits landed."), "1 sentences"},
		{"five sentences", nar("Enter internal/git", "Six commits landed. Then more. And more. Still more. The end."), "5 sentences"},
		{"long text", nar("Enter internal/git", "Six commits landed. "+strings.Repeat("Then more work. ", 60)), "characters"},
		{"exclamation", nar("Enter internal/git", "Six commits landed! It was a lot."), "exclamation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := validate(f, base, tc.n)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(v, func(s string) bool { return strings.Contains(s, tc.want) }) {
				t.Errorf("violations %q, want one mentioning %q", v, tc.want)
			}
		})
	}
}

// TestValidateGeneric checks that facts reach the validator through their
// JSON, so that fields it does not know about count.
func TestValidateGeneric(t *testing.T) {
	type more struct {
		Facts
		Authors []string
	}
	f := chapter()
	v, err := newVocabulary(f, Narration{})
	if err != nil {
		t.Fatal(err)
	}
	if !v.numbers["14"] || !v.words["October"] || !v.numbers["9"] || !strings.Contains(v.text, "(*Repo).Walk") {
		t.Errorf("vocabulary misses facts: numbers %v", v.numbers)
	}
	// The same walk over a type with more fields sees them.
	raw, _ := json.Marshal(more{f, []string{"Ada Lovelace"}})
	if !strings.Contains(string(raw), "Ada Lovelace") || strings.Contains(string(raw), "Beginnings") {
		t.Errorf("facts JSON = %s", raw)
	}
}

// fakeAPI answers Messages API requests with the given narrations in turn,
// then with the last one, and records what it was sent.
type fakeAPI struct {
	mu       sync.Mutex
	answers  []any // Narration, or an int status code
	requests []map[string]any
	beta     []string
}

func (a *fakeAPI) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("request body: %v", err)
		}
		a.mu.Lock()
		i := min(len(a.requests), len(a.answers)-1)
		a.requests = append(a.requests, req)
		a.beta = append(a.beta, r.Header.Get("anthropic-beta"))
		answer := a.answers[i]
		a.mu.Unlock()

		if code, ok := answer.(int); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"down"}}`)
			return
		}
		n := answer.(Narration)
		text, _ := json.Marshal(narration{n.Title, n.Text})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_test", "type": "message", "role": "assistant", "model": req["model"],
			"content":     []any{map[string]any{"type": "text", "text": string(text)}},
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (a *fakeAPI) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func newTestClaude(t *testing.T, a *fakeAPI, cache string) (*Claude, *[]string) {
	srv := a.serve(t)
	c := NewClaude("", cache, option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
	var logs []string
	var mu sync.Mutex
	c.Log = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, format)
	}
	return c, &logs
}

var goodNarration = Narration{
	Title: "Enter internal/git",
	Text:  "Over 6 commits, internal/git arrived with 9 symbols. It drew 18 of the 25 semantic edits.",
}

func TestClaudeNarrates(t *testing.T) {
	a := &fakeAPI{answers: []any{goodNarration}}
	c, logs := newTestClaude(t, a, "")
	got, err := c.Narrate(context.Background(), chapter())
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != goodNarration.Title || got.Text != goodNarration.Text || got.Model != DefaultModel {
		t.Errorf("narration = %+v", got)
	}
	if len(*logs) != 0 {
		t.Errorf("logs = %q", *logs)
	}

	req := a.requests[0]
	if req["model"] != DefaultModel {
		t.Errorf("model = %v", req["model"])
	}
	system := req["system"].([]any)[0].(map[string]any)
	if system["cache_control"] == nil || !strings.Contains(system["text"].(string), "State only what the facts contain") {
		t.Errorf("system = %v", system)
	}
	format := req["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("format = %v", format)
	}
	if !strings.Contains(a.beta[0], "server-side-fallback") || req["fallbacks"] != "default" {
		t.Errorf("fallbacks: header %q, body %v", a.beta[0], req["fallbacks"])
	}
	// The facts go as JSON, with the baseline and the earlier titles.
	var sent struct {
		Chapter  map[string]any `json:"chapter"`
		Previous []string       `json:"previous_chapter_titles"`
		Baseline narration      `json:"baseline"`
	}
	content := req["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if err := json.Unmarshal([]byte(content), &sent); err != nil {
		t.Fatalf("user message is not the request JSON: %v\n%s", err, content)
	}
	base, _ := Templates{}.Narrate(context.Background(), chapter())
	if sent.Chapter["Hottest"] != "internal/git" || sent.Chapter["Previous"] != nil ||
		!slices.Equal(sent.Previous, []string{"Beginnings: cmd/huginn"}) || sent.Baseline.Text != base.Text {
		t.Errorf("sent %+v", sent)
	}
	if m := sent.Chapter["Moments"].([]any)[0].(map[string]any); m["Kind"] != "birth" {
		t.Errorf("moment kind = %v", m["Kind"])
	}
}

func TestClaudeRetries(t *testing.T) {
	bad := Narration{Title: "Enter internal/git", Text: "Ada Lovelace added 120 symbols. It was fast."}
	a := &fakeAPI{answers: []any{bad, goodNarration}}
	c, logs := newTestClaude(t, a, "")
	got, err := c.Narrate(context.Background(), chapter())
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != goodNarration.Text || a.calls() != 2 || len(*logs) != 0 {
		t.Errorf("narration %+v after %d calls, logs %q", got, a.calls(), *logs)
	}
	retry := a.requests[1]["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(retry, "the number 120 is not in the facts") || !strings.Contains(retry, `"Lovelace"`) {
		t.Errorf("retry does not list the violations:\n%s", retry)
	}
}

func TestClaudeFallsBack(t *testing.T) {
	base, _ := Templates{}.Narrate(context.Background(), chapter())
	bad := nar("A massive leap", "Everything changed!")
	for name, answers := range map[string][]any{
		"invalid twice": {bad, bad},
		"api error":     {http.StatusInternalServerError},
		"bad request":   {http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			a := &fakeAPI{answers: answers}
			c, logs := newTestClaude(t, a, t.TempDir())
			got, err := c.Narrate(context.Background(), chapter())
			if err != nil {
				t.Fatal(err)
			}
			if got != base {
				t.Errorf("narration = %+v, want the template's", got)
			}
			if len(*logs) != 1 || !strings.Contains((*logs)[0], "fell back") {
				t.Errorf("logs = %q", *logs)
			}
			if want := min(2, len(answers)); a.calls() != want {
				t.Errorf("%d calls, want %d", a.calls(), want)
			}
			// Fallbacks are not cached: the next run asks again.
			c.Narrate(context.Background(), chapter())
			if a.calls() == min(2, len(answers)) {
				t.Error("a fallback was cached")
			}
		})
	}
}

func TestClaudeCaches(t *testing.T) {
	dir := t.TempDir()
	a := &fakeAPI{answers: []any{goodNarration}}
	c, _ := newTestClaude(t, a, dir)
	ctx := context.Background()
	first, _ := c.Narrate(ctx, chapter())
	again, _ := c.Narrate(ctx, chapter())
	if a.calls() != 1 || again != first {
		t.Errorf("%d calls; cached narration %+v, first %+v", a.calls(), again, first)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "narrations", "*.json"))
	if len(files) != 1 {
		t.Errorf("cache files = %q", files)
	}

	// Other facts, another model or other earlier chapters miss the cache.
	f := chapter()
	f.Commits = 7
	c.Narrate(ctx, f)
	f = chapter()
	f.Previous = nil
	c.Narrate(ctx, f)
	c.Model = "claude-sonnet-5-5"
	c.Narrate(ctx, chapter())
	if a.calls() != 4 {
		t.Errorf("%d calls, want 4", a.calls())
	}

	// A new narrator over the same directory reads it.
	b := &fakeAPI{answers: []any{http.StatusInternalServerError}}
	c2, _ := newTestClaude(t, b, dir)
	if got, _ := c2.Narrate(ctx, chapter()); got != first || b.calls() != 0 {
		t.Errorf("narration %+v after %d calls", got, b.calls())
	}

	// A corrupt entry is narrated afresh.
	for _, p := range files {
		os.WriteFile(p, []byte("{"), 0o644)
	}
	c2.Narrate(ctx, chapter())
	if b.calls() != 1 {
		t.Errorf("corrupt cache entry was used")
	}
}

func TestClaudeCancelled(t *testing.T) {
	a := &fakeAPI{answers: []any{goodNarration}}
	c, _ := newTestClaude(t, a, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Narrate(ctx, chapter()); err == nil {
		t.Error("no error for a cancelled context")
	}
}

// numbered narrates by chapter number, failing on chapter 2.
type numbered struct{ calls chan int }

func (n numbered) Narrate(_ context.Context, f Facts) (Narration, error) {
	n.calls <- f.Number
	if f.Number == 2 {
		return Narration{}, context.DeadlineExceeded
	}
	return Narration{Title: "T" + string(rune('0'+f.Number)), Text: "x", Model: "m"}, nil
}

func TestRetell(t *testing.T) {
	st := &Story{Moments: []Moment{}}
	for i := 1; i <= 3; i++ {
		st.Chapters = append(st.Chapters, Chapter{Facts{Number: i, Of: 3}, Narration{Title: "old", Text: "old"}})
	}
	n := numbered{calls: make(chan int, 3)}
	out, err := Retell(context.Background(), st, n, 2)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, c := range out.Chapters {
		titles = append(titles, c.Title)
	}
	if !slices.Equal(titles, []string{"T1", "old", "T3"}) || st.Chapters[0].Title != "old" || len(n.calls) != 3 {
		t.Errorf("retold titles %q; original %q", titles, st.Chapters[0].Title)
	}
}
