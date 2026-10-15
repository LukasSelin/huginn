package web

import (
	"context"
	"encoding/json"
	"fmt"
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
	for _, url := range []string{"/api/dirs", "/api/dir?d=geo", "/api/symbol/1", "/api/health", "/api/journey"} {
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
			!slices.Equal(edit.Flags, []string{"signature", "body"}) || edit.Stmts != 2 ||
			edit.Rev != 2 || edit.Path != "geo/point.go" || edit.Name != "(*Point).Move" ||
			edit.Signature != "func (*Point) Move(dx, dy int)" || edit.OldPath != "geo/point.go" || edit.OldName != "(*Point).Move" ||
			sym.Events[0].OldName != "" {
			t.Errorf("edit event = %+v", edit)
		}

		getJSON(t, h, "/api/symbol/999", http.StatusNotFound, nil)
		getJSON(t, h, "/api/symbol/zero", http.StatusNotFound, nil)
	})

	t.Run("health", func(t *testing.T) {
		var hj healthJSON
		getJSON(t, h, "/api/health", http.StatusOK, &hj)
		if len(hj.Buckets) != 3 {
			t.Fatalf("buckets = %+v, want one per commit", hj.Buckets)
		}
		var exported []int
		for _, b := range hj.Buckets {
			exported = append(exported, b.Exported)
		}
		// Point and Move; then Hello and Bye; then Bye is gone.
		if want := []int{2, 4, 3}; !slices.Equal(exported, want) {
			t.Errorf("exported = %v, want %v", exported, want)
		}
		last := hj.Buckets[2]
		if last.Last != hashes[2] || last.Symbols != 4 || last.Reshaped != 1 || last.Edits["deleted"] != 1 {
			t.Errorf("last bucket = %+v", last)
		}
		var paths []string
		for _, s := range hj.Hotspots {
			paths = append(paths, s.Path)
		}
		// Both geo files have three edits on complexity 1; main.go one.
		if want := []string{"geo/hello.go", "geo/point.go", "main.go"}; !slices.Equal(paths, want) {
			t.Errorf("hotspots = %q, want %q", paths, want)
		}
		if hj.Tests || hj.Authors != 1 || hj.BusFactor != 1 || len(hj.Owners) != 1 || hj.Owners[0].Symbols != 4 {
			t.Errorf("tests %v, authors %d, bus factor %d, owners %+v", hj.Tests, hj.Authors, hj.BusFactor, hj.Owners)
		}
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
		// One author knows every live package wholly.
		if len(j.Authors) != 1 {
			t.Errorf("authors = %q", j.Authors)
		}
		for _, p := range j.Frames[2].Pkgs {
			if p.Live > 0 && (len(p.Knowers) != 1 || p.Knowers[0] != [3]float64{0, 1, 0} || p.Bus != 1 || p.Orphaned != 0) {
				t.Errorf("package %d knowers %v bus %d orphaned %v", p.P, p.Knowers, p.Bus, p.Orphaned)
			}
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

	t.Run("journey package", func(t *testing.T) {
		var v symViewJSON
		getJSON(t, h, "/api/journey/package/geo?step=commit", http.StatusOK, &v)
		if v.Dir != "geo" || v.Step != "commit" || v.Total != 4 || v.Truncated || len(v.Symbols) != 4 || len(v.Frames) != 3 {
			t.Fatalf("view = %+v", v)
		}
		name := func(i int) string { return v.Symbols[i].Name }
		var got []string
		for _, f := range v.Frames {
			var s []string
			for _, z := range f.Set {
				s = append(s, fmt.Sprintf("+%s:%d", name(z[0]), z[1]))
			}
			for _, k := range f.Gone {
				s = append(s, "-"+name(k))
			}
			for _, e := range f.Ev {
				s = append(s, e.Kind+" "+e.Name+" "+e.OldSig+e.Sig)
			}
			got = append(got, strings.Join(s, ", "))
		}
		// Symbols are in file order: hello.go, then point.go.
		want := []string{
			"+Point:0, +(*Point).Move:1, born Point , born (*Point).Move ",
			"+Hello:1, +Bye:0, born Hello , born Bye ",
			"+(*Point).Move:2, -Bye, died Bye , signature (*Point).Move func (*Point) Move(dx int)func (*Point) Move(dx, dy int)",
		}
		if !slices.Equal(got, want) {
			t.Errorf("frames:\n%q\nwant\n%q", got, want)
		}

		var root symViewJSON
		getJSON(t, h, "/api/journey/package/?step=week", http.StatusOK, &root)
		if root.Dir != "." || len(root.Symbols) != 1 || root.Symbols[0].Name != "main" {
			t.Errorf("root = %+v", root)
		}
		getJSON(t, h, "/api/journey/package/nowhere", http.StatusNotFound, nil)
		getJSON(t, h, "/api/journey/package/geo?step=day", http.StatusBadRequest, nil)
	})

	t.Run("journey symbols", func(t *testing.T) {
		var hits []symHitJSON
		getJSON(t, h, "/api/journey/symbols?q=MO", http.StatusOK, &hits)
		if len(hits) != 1 || hits[0].Name != "(*Point).Move" || hits[0].Dir != "geo" || !hits[0].Alive || hits[0].Kind != "method" {
			t.Errorf("hits for MO = %+v", hits)
		}
		// Exact before prefix before the rest; live before deleted.
		getJSON(t, h, "/api/journey/symbols?q=point", http.StatusOK, &hits)
		var names []string
		for _, x := range hits {
			names = append(names, x.Name)
		}
		if !slices.Equal(names, []string{"Point", "(*Point).Move"}) {
			t.Errorf("hits for point = %q", names)
		}
		getJSON(t, h, "/api/journey/symbols?q=b", http.StatusOK, &hits)
		if len(hits) != 1 || hits[0].Name != "Bye" || hits[0].Alive {
			t.Errorf("hits for b = %+v", hits)
		}
		getJSON(t, h, "/api/journey/symbols?q=", http.StatusOK, &hits)
		if len(hits) != 0 {
			t.Errorf("hits for nothing = %+v", hits)
		}
	})

	t.Run("ui", func(t *testing.T) {
		for _, page := range []string{"/", "/health", "/journey"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page, nil))
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != http.StatusOK || !strings.Contains(string(body), "<html") {
				t.Errorf("GET %s: status %d, body %.80q", page, rec.Code, body)
			}
		}
	})
}
