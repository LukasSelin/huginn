package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"huginn/internal/git"
	"huginn/internal/gittest"
	"huginn/internal/lineage"
)

// newServer returns a server over a small repository, not yet indexed, and
// the hashes of its commits.
func newServer(t *testing.T) (*Server, []string) {
	t.Helper()
	r := gittest.New(t)
	hashes := []string{
		r.Commit("root", map[string]string{
			"main.go":           "package main\n\nfunc main() { geo.Hello() }\n",
			"geo/point.go":      "package geo\n\ntype Point struct{ X, Y int }\n\nfunc (p *Point) Move(dx int) { p.X += dx }\n",
			"geo/point_test.go": "package geo\n\nfunc TestMove() {}\n",
		}),
		r.Commit("add Hello", map[string]string{
			"geo/hello.go": "package geo\n\nfunc Hello() string { return \"hi\" }\n\nfunc Bye() {}\n",
		}),
		r.Commit("edit Move, drop Bye", map[string]string{
			"geo/point.go": "package geo\n\ntype Point struct{ X, Y int }\n\nfunc (p *Point) Move(dx, dy int) {\n\tp.X += dx\n\tp.Y += dy\n}\n",
			"geo/hello.go": "package geo\n\nfunc Hello() string { return \"hi\" }\n",
		}),
	}
	repo, err := git.Open(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return New(repo, "demo", lineage.BuildOptions{}), hashes
}

// get serves one request and decodes a JSON response into v, if given.
func getJSON(t *testing.T, h http.Handler, url string, wantCode int, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != wantCode {
		t.Fatalf("GET %s: status %d, want %d: %s", url, rec.Code, wantCode, rec.Body)
	}
	if v == nil {
		return
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET %s: Content-Type %q", url, ct)
	}
	if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
}

func TestBeforeIndex(t *testing.T) {
	srv, _ := newServer(t)
	h := srv.Handler()

	var st statusJSON
	getJSON(t, h, "/api/status", http.StatusOK, &st)
	if st.Ready || st.Repo != "demo" {
		t.Errorf("status = %+v, want not ready for demo", st)
	}
	for _, url := range []string{"/api/dirs", "/api/dir?d=geo", "/api/symbol/1", "/api/journey"} {
		getJSON(t, h, url, http.StatusServiceUnavailable, nil)
	}
}

func TestIndexError(t *testing.T) {
	srv, _ := newServer(t)
	srv.opts.Branch = "no-such-branch"
	if err := srv.Index(context.Background()); err == nil {
		t.Fatal("Index succeeded on a missing branch")
	}
	var st statusJSON
	getJSON(t, srv.Handler(), "/api/status", http.StatusOK, &st)
	if st.Ready || st.Error == "" {
		t.Errorf("status = %+v, want an error", st)
	}
}

func TestAPI(t *testing.T) {
	srv, hashes := newServer(t)
	if err := srv.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	t.Run("status", func(t *testing.T) {
		var st statusJSON
		getJSON(t, h, "/api/status", http.StatusOK, &st)
		// main, Point, (*Point).Move and Hello are live; Bye was deleted.
		// TestMove is left out with the other test symbols.
		want := statusJSON{Repo: "demo", Ready: true, Commits: 3, Live: 4, Symbols: 5, TookMS: st.TookMS}
		want.From, want.To = st.From, st.To
		if st != want {
			t.Errorf("status = %+v, want %+v", st, want)
		}
		if day := int64(24 * 60 * 60); st.To-st.From != 2*day {
			t.Errorf("span %d..%d, want two days", st.From, st.To)
		}
	})

	t.Run("dirs", func(t *testing.T) {
		var dirs []dirJSON
		getJSON(t, h, "/api/dirs", http.StatusOK, &dirs)
		want := []dirJSON{
			{Dir: ".", Live: 1, Changes: 1},
			// Point 1, Move 2, Hello 1, Bye 2.
			{Dir: "geo", Live: 3, Changes: 6},
		}
		if !slices.Equal(dirs, want) {
			t.Errorf("dirs = %+v, want %+v", dirs, want)
		}
	})

	var lanes struct {
		Dir     string     `json:"dir"`
		Symbols []laneJSON `json:"symbols"`
	}
	t.Run("dir", func(t *testing.T) {
		getJSON(t, h, "/api/dir?d=geo", http.StatusOK, &lanes)
		var got []string
		for _, l := range lanes.Symbols {
			var marks []string
			for _, m := range l.Marks {
				marks = append(marks, m.Kind)
			}
			got = append(got, l.Name+" "+l.File+" "+strings.Join(marks, ","))
		}
		// Live first, then by file and line.
		want := []string{
			"Hello hello.go added",
			"Point point.go added",
			"(*Point).Move point.go added,signature",
			"Bye hello.go added,deleted",
		}
		if lanes.Dir != "geo" || !slices.Equal(got, want) {
			t.Errorf("dir %q = %q, want %q", lanes.Dir, got, want)
		}
		getJSON(t, h, "/api/dir?d=nowhere", http.StatusNotFound, nil)
	})

	t.Run("symbol", func(t *testing.T) {
		var move lineage.ID
		for _, l := range lanes.Symbols {
			if l.Name == "(*Point).Move" {
				move = l.ID
			}
		}
		if move == 0 {
			t.Fatal("no lane for (*Point).Move")
		}
		var sym symbolJSON
		getJSON(t, h, "/api/symbol/"+strconv.FormatInt(int64(move), 10), http.StatusOK, &sym)
		if sym.Name != "(*Point).Move" || sym.Kind != "method" || sym.Path != "geo/point.go" ||
			sym.Start != 5 || sym.End != 8 || !sym.Alive || sym.Authors != 1 ||
			sym.Signature != "func (*Point) Move(dx, dy int)" {
			t.Errorf("symbol = %+v", sym)
		}
		if len(sym.Events) != 2 {
			t.Fatalf("events = %+v, want 2", sym.Events)
		}
		edit := sym.Events[1]
		if edit.SHA != hashes[2] || edit.Author != "Ada" || edit.Kind != "signature" ||
			!slices.Equal(edit.Flags, []string{"signature", "body"}) || edit.Stmts != 2 {
			t.Errorf("edit event = %+v", edit)
		}

		getJSON(t, h, "/api/symbol/999", http.StatusNotFound, nil)
		getJSON(t, h, "/api/symbol/zero", http.StatusNotFound, nil)
	})

	t.Run("journey", func(t *testing.T) {
		var j journeyJSON
		getJSON(t, h, "/api/journey?step=commit", http.StatusOK, &j)
		if j.Step != "commit" || len(j.Frames) != 3 || len(j.Packages) != 2 {
			t.Fatalf("journey = %+v", j)
		}
		var lives []int
		for _, f := range j.Frames {
			lives = append(lives, f.Live)
		}
		// main and Point, Move; then Hello and Bye; then Bye goes.
		if !slices.Equal(lives, []int{3, 5, 4}) || j.Frames[2].SHA != hashes[2] || j.Frames[2].Total != 3 {
			t.Errorf("frames = %+v", j.Frames)
		}
		if len(j.Chapters) != 3 || j.Chapters[0].FirstRev != 0 || j.Chapters[2].LastRev != 2 || j.Chapters[0].Title == "" {
			t.Errorf("chapters = %+v", j.Chapters)
		}
		getJSON(t, h, "/api/journey", http.StatusOK, &j)
		if j.Step != "week" || len(j.Frames) == 0 {
			t.Errorf("default step %q, %d frames", j.Step, len(j.Frames))
		}
		getJSON(t, h, "/api/journey?step=day", http.StatusBadRequest, nil)
	})

	t.Run("ui", func(t *testing.T) {
		for _, page := range []string{"/", "/journey"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page, nil))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK || !strings.Contains(string(body), "<html") {
				t.Errorf("GET %s: status %d, body %.80q", page, rec.Code, body)
			}
		}
	})
}
