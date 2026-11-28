package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"movielist-app/internal/ai"
	"movielist-app/internal/storage"
	"movielist-app/internal/tmdb"
)

type r10bIdentity struct {
	kind string
	id   int
}

func (id r10bIdentity) String() string {
	if id.id == 0 {
		return "U"
	}
	return fmt.Sprintf("%s:%d", id.kind, id.id)
}

type r10bWork struct {
	id                  r10bIdentity
	year                int
	original, localized string
	alias               string
}

type r10bFixture struct {
	name, filename, groups string
	year                   int
	truth, oracle          r10bIdentity
	provenance             string
	first, second          ai.RecognizedTitle
	catalogue              []r10bWork
	selected               r10bIdentity
	accept, review         string // Expected policy outcomes, separate from the oracle.
}

func r10bMovie(id int) r10bIdentity { return r10bIdentity{"movie", id} }

func r10bFixtures() []r10bFixture {
	const year = 2024
	movie := func(id int, original, localized string) r10bWork {
		return r10bWork{id: r10bMovie(id), year: year, original: original, localized: localized}
	}
	fixture := func(name, filename, groups string, truth, oracle r10bIdentity, provenance, title, accept, review string, works ...r10bWork) r10bFixture {
		return r10bFixture{
			name: name, filename: filename, groups: groups, year: year, truth: truth, oracle: oracle, provenance: provenance,
			first:     ai.RecognizedTitle{ENTitle: "Uncertain First Guess", MediaType: "movie", Confidence: 0.2, Status: "resolved"},
			second:    ai.RecognizedTitle{ENTitle: title, MediaType: "movie", Confidence: 0.98, Status: "resolved"},
			catalogue: works, selected: works[0].id, accept: accept, review: review,
		}
	}
	positive := func(truth int, name, filename, groups, provenance, title, accept string, work r10bWork) r10bFixture {
		identity := r10bMovie(truth)
		return fixture(name, filename, groups, identity, identity, provenance, title, accept, "", work)
	}
	empty := r10bIdentity{}
	languageSame := "same no_localization"
	languageDifferent := "different no_localization"
	twin := movie(9109, "Paper Lantern Brigade", "")
	guarding := r10bWork{id: r10bMovie(1551025), year: 2026, original: "Guarding Stars", localized: "Охрана звёзд"}
	guardingBare := guarding
	guardingBare.localized = ""
	guardingAlias := guardingBare
	guardingAlias.original, guardingAlias.alias = "Stellar Patrol", "Guarding Stars"
	alias := movie(9110, "Чужа планета", "")
	alias.alias = "Other Planet"
	remake := movie(9120, "Winter Census", "")
	remake.year = 2023
	tv := r10bWork{id: r10bIdentity{"tv", 9122}, year: year, original: "Harbour District"}
	yearConflict := movie(9126, "Signal Street", "")
	yearConflict.year = 2020
	items := []r10bFixture{
		positive(9101, "exact", "Ivory Compass", languageSame, "Master label and fixture manifest identify the same cut.", "Ivory Compass", "ABC", movie(9101, "Ivory Compass", "")),
		positive(9101, "punctuation", "Ivory: Compass", languageSame, "The master label separates the title with punctuation.", "Ivory Compass", "ABC", movie(9101, "Ivory Compass", "")),
		positive(9101, "case", "IVORY COMPASS", languageSame, "The same master label is written in uppercase.", "Ivory Compass", "ABC", movie(9101, "Ivory Compass", "")),
		positive(9101, "word_order", "Compass Ivory", languageSame, "The release manifest reverses the words of the master label.", "Ivory Compass", "B", movie(9101, "Ivory Compass", "")),
		positive(9102, "short_title", "Ivory Compass", languageSame, "The distributor labels the extended-title master by its short name.", "Ivory Compass Returns", "AB", movie(9102, "Ivory Compass Returns", "")),
		positive(9103, "russian_title", "Серебряный город", languageDifferent, "The Russian release label in the master manifest is paired with a different international title.", "Distant Signal", "B", movie(9103, "Distant Signal", "")),
		positive(9103, "ukrainian_title", "Срібне місто", languageDifferent, "The Ukrainian distributor manifest pairs this release with the international master.", "Distant Signal", "B", movie(9103, "Distant Signal", "")),
		positive(9104, "localized_exact", "Місто тиші", "different localization", "The Ukrainian release label matches the master and the catalogue localization.", "The Long Crossing", "ABC", movie(9104, "The Long Crossing", "Місто тиші")),
		positive(9104, "localized_other", "Після припливу", "different localization", "A second Ukrainian distributor renamed the same master; the catalogue shows the first distributor's title.", "The Long Crossing", "B", movie(9104, "The Long Crossing", "Місто тиші")),
		positive(9105, "localized_fallback_original", "Дім біля озера", languageDifferent, "The Ukrainian release manifest identifies the master; the catalogue display falls back to its international title.", "A Place Apart", "B", movie(9105, "A Place Apart", "A Place Apart")),
		positive(9110, "alias_exact", "Other Planet", "different no_localization", "The international distributor's title in the manifest matches the selected catalogue alias.", "Other Planet", "ABC", alias),
		positive(1551025, "guarding_localized", "Охрана звёзд", "different localization", "The task supplies the manually confirmed identity; localization is mocked.", "Guarding Stars", "ABC", guarding),
		positive(1551025, "guarding_bare", "Охрана звёзд", languageDifferent, "The same manually confirmed work; the mock lacks both localized title and alias.", "Guarding Stars", "B", guardingBare),
		positive(1551025, "guarding_alias", "Охрана звёзд", languageDifferent, "The same manually confirmed work; the mock exposes the international title only through an alias.", "Guarding Stars", "B", guardingAlias),
		positive(1551025, "guarding_e", "Охрана звезд", "different localization transliteration", "The master is identical to the Guarding Stars fixture, with е on its release label.", "Guarding Stars", "AB", guarding),
		positive(9111, "cyrillic_filename", "Скарпетта", languageDifferent+" transliteration", "The manifest pairs the Cyrillic release label with the romanized master title.", "Skarpetta", "BC", movie(9111, "Skarpetta", "")),
		positive(9112, "latin_filename", "Okhrana zvyozd", languageDifferent+" transliteration", "The Latin label is a romanization of the same source master.", "Охрана звёзд", "BC", movie(9112, "Охрана звёзд", "")),
		positive(9113, "phonetic_variant", "Скарпетта", languageDifferent+" transliteration", "The English distributor uses Sc while the source label uses Sk in direct romanization.", "Scarpetta", "B", movie(9113, "Scarpetta", "")),
		fixture("transliteration_collision", "Мир", languageDifferent+" transliteration", r10bMovie(9201), empty, "The fixture manifest assigns this label to a different master; only an unrelated work with a colliding romanization is returned.", "Mir", "BC", "", movie(9114, "Mir", "")),
		fixture("different_work", "Берег вітрів", languageDifferent, r10bMovie(9202), empty, "The source manifest identifies an absent work; the offered work has the same year and type.", "Plausible Harbour", "B", "", movie(9115, "Plausible Harbour", "")),
		fixture("similar_title", "Northern Signals", languageSame+" similar", r10bMovie(9203), empty, "The source manifest assigns the plural title to a different master from the singular catalogue title.", "Northern Signal", "AB", "", movie(9116, "Northern Signal", "")),
		fixture("different_director", "Маршрут додому", "different localization similar", r10bMovie(9204), empty, "The source master is credited to director A; the offered unrelated master is credited to director B in the fixture provenance.", "Homecoming Route", "B", "", movie(9117, "Homecoming Route", "Дорога додому")),
		fixture("different_country", "Острів туману", "different localization similar", r10bMovie(9205), empty, "The fixture manifest identifies a Polish master; the similar catalogue title belongs to a Canadian master.", "The Fog Island", "AB", "", movie(9118, "The Fog Island", "Острів туманів")),
		fixture("remake", "Winter Census", languageSame+" homonyms", r10bMovie(9120), r10bMovie(9120), "The release uses the prior year's original under a mislabeled filename year; the new remake is also present.", "Winter Census", "ABC", "", movie(9121, "Winter Census", ""), remake),
		fixture("homonym_tie", "Silent Bridge", languageSame+" homonyms", r10bMovie(9125), r10bMovie(9125), "The manifest identifies the higher-ID work; both masters have identical catalogue title, year and type.", "Silent Bridge", "AB", "AB", movie(9124, "Silent Bridge", ""), movie(9125, "Silent Bridge", "")),
		fixture("movie_tv_overlap", "Harbour District", languageSame+" homonyms", tv.id, tv.id, "The source manifest identifies the TV master; the movie has the same title, year and numeric ID.", "Harbour District", "ABC", "", movie(9122, "Harbour District", ""), tv),
		fixture("year_conflict", "Signal Street", languageSame, r10bMovie(9206), empty, "The returned identically titled work predates the source master by four years; the source master is absent.", "Signal Street", "", "", yearConflict),
		positive(9109, "indistinguishable_correct", "Паперове військо", languageDifferent, "The hidden source manifest identifies the offered master.", twin.original, "B", twin),
		fixture("indistinguishable_wrong", "Паперове військо", languageDifferent, r10bMovie(9207), empty, "A different master carries the same release label, year and type. It is absent from the visible responses.", twin.original, "B", "", twin),
	}
	for i := range items {
		f := &items[i]
		if strings.HasPrefix(f.name, "guarding_") {
			f.year = 2026
			f.first.ENTitle, f.first.Confidence, f.first.Status = "", 0, "unresolved"
		}
		year := f.year
		f.first.Year, f.second.Year = &year, &year
		// Use a filename that is legal on Windows.
		f.filename = strings.ReplaceAll(f.filename, ":", " -")
		f.filename = fmt.Sprintf("%s.%d.mkv", f.filename, f.year)
	}
	return items
}

type r10bCounts struct {
	search, details, aliases, primary, semantic atomic.Int32
}

type r10bResult struct {
	correct, fp, fn, unresolved, review                  int
	initial, search, details, aliases, primary, semantic int
}

func (r *r10bResult) add(s r10bResult) {
	r.correct += s.correct
	r.fp += s.fp
	r.fn += s.fn
	r.unresolved += s.unresolved
	r.review += s.review
	r.initial += s.initial
	r.search += s.search
	r.details += s.details
	r.aliases += s.aliases
	r.primary += s.primary
	r.semantic += s.semantic
}

func (r r10bResult) String() string {
	return fmt.Sprintf("%d/%d/%d/%d/%d | %d | %d/%d/%d | %d/%d",
		r.correct, r.fp, r.fn, r.unresolved, r.review, r.initial, r.search, r.details, r.aliases, r.primary, r.semantic)
}

func TestR10BFilenameVerification(t *testing.T) {
	variant := os.Getenv("MOVIELIST_R10B_VARIANT")
	if variant == "" {
		variant = "A"
	}
	if variant != "A" && variant != "B" && variant != "C" {
		t.Fatalf("invalid experimental variant %q", variant)
	}
	fixtures := r10bFixtures()
	twinCorrect, twinWrong := fixtures[len(fixtures)-2], fixtures[len(fixtures)-1]
	if twinCorrect.filename != twinWrong.filename || twinCorrect.year != twinWrong.year ||
		*twinCorrect.first.Year != *twinWrong.first.Year || *twinCorrect.second.Year != *twinWrong.second.Year ||
		!slices.Equal(twinCorrect.catalogue, twinWrong.catalogue) || twinCorrect.truth == twinWrong.truth {
		t.Fatal("indistinguishable fixtures must share inputs and differ in hidden identity")
	}
	// The only differences in the serialized hypotheses could otherwise be pointer identity.
	for _, pair := range [][2]ai.RecognizedTitle{{twinCorrect.first, twinWrong.first}, {twinCorrect.second, twinWrong.second}} {
		if string(r10bJSON(pair[0])) != string(r10bJSON(pair[1])) {
			t.Fatal("indistinguishable fixtures have different hypotheses")
		}
	}

	var models sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model, _, _ := strings.Cut(filepath.Base(r.URL.Path), ":")
		handler, ok := models.Load(model)
		if !ok {
			t.Errorf("unexpected AI model %q", model)
			http.Error(w, "fixture", http.StatusBadRequest)
			return
		}
		handler.(http.HandlerFunc)(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv("GOOGLE_GEMINI_BASE_URL", server.URL)

	results := make([]r10bResult, len(fixtures))
	wires := make([][]string, len(fixtures))
	t.Run("fixtures", func(t *testing.T) {
		for i, f := range fixtures {
			t.Run(f.name, func(t *testing.T) {
				if f.name != twinCorrect.name && f.name != twinWrong.name {
					t.Parallel()
				}
				available := slices.ContainsFunc(f.catalogue, func(w r10bWork) bool { return w.id == f.truth })
				if f.provenance == "" || (available && f.oracle != f.truth) || (!available && f.oracle.id != 0) {
					t.Fatalf("invalid independent oracle for %s", f.name)
				}
				got, r, wire := runR10BFixture(t, f, &models)
				want := r10bIdentity{}
				if strings.Contains(f.accept, variant) {
					want = f.selected
				}
				wantReview := want.id == 0 || strings.Contains(f.review, variant)
				if got != want || (r.review == 1) != wantReview {
					t.Errorf("variant %s got %s review=%d, want %s review=%t", variant, got, r.review, want, wantReview)
				}
				t.Logf("R10B %s %s: oracle=%s got=%s | %s", variant, f.name, f.oracle, got, r)
				results[i], wires[i] = r, wire
			})
		}
	})
	correctWire, wrongWire := wires[len(wires)-2], wires[len(wires)-1]
	if correctWire != nil && wrongWire != nil && !slices.Equal(correctWire, wrongWire) {
		t.Error("indistinguishable fixtures produced different provider requests or responses")
	}
	groups := map[string]r10bResult{}
	for i, f := range fixtures {
		for _, group := range append(strings.Fields(f.groups), "all") {
			total := groups[group]
			total.add(results[i])
			groups[group] = total
		}
	}
	for _, group := range []string{"same", "different", "no_localization", "localization", "homonyms", "similar", "transliteration", "all"} {
		t.Logf("R10B %s group %s: %s", variant, group, groups[group])
	}
}

func runR10BFixture(t *testing.T, f r10bFixture, models *sync.Map) (r10bIdentity, r10bResult, []string) {
	a, dir := newTestAppDB(t, nil)
	a.cfg.MediaFolderPath = dir
	a.cfg.PostersDir = t.TempDir()
	a.cfg.TMDBAPIKey, a.cfg.GeminiAPIKey = "fixture-tmdb", "fixture-gemini"
	modelPrefix := "gemini-r10b-" + strings.ReplaceAll(f.name, "_", "-")
	if strings.HasPrefix(f.name, "indistinguishable_") {
		// Keep the model identities identical in both transcripts.
		modelPrefix = "gemini-r10b-indistinguishable"
	}
	primary, secondary := modelPrefix+"-flash", modelPrefix+"-pro"
	a.cfg.GeminiModels = []string{primary, secondary}
	a.tmdbClient = tmdb.NewClient(a.cfg)
	t.Cleanup(a.tmdbClient.Close)
	a.aiClient = ai.NewClient(a.cfg)
	// A fixed generic parent makes both twins' AI hints and exact scoring evidence identical.
	path := filepath.Join(dir, "Movies", f.filename)
	counts := &r10bCounts{}
	var wireMu sync.Mutex
	var wire []string
	record := func(request, response string) {
		wireMu.Lock()
		defer wireMu.Unlock()
		wire = append(wire, request, response)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Contents []struct{ Parts []struct{ Text string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 {
			t.Error("invalid Gemini request")
			http.Error(w, "fixture", http.StatusBadRequest)
			return
		}
		prompt := req.Contents[0].Parts[0].Text
		_, jsonLine, _ := strings.Cut(prompt, "do not re-guess them):\n")
		var inputs []ai.FileRecognitionContext
		if err := json.Unmarshal([]byte(strings.SplitN(jsonLine, "\n", 2)[0]), &inputs); err != nil || len(inputs) != 1 {
			t.Error("expected a single recognition context")
			http.Error(w, "fixture", http.StatusBadRequest)
			return
		}
		input := inputs[0]
		if input.RequestID != "recognition-0" || input.OriginalFile != f.filename || input.Year != f.year {
			t.Errorf("unexpected Gemini context: %+v", input)
		}
		row := f.first
		switch filepath.Base(r.URL.Path) {
		case primary + ":generateContent":
			counts.primary.Add(1)
		case secondary + ":generateContent":
			counts.semantic.Add(1)
			wantRejection := "low_confidence"
			if f.first.ENTitle == "" {
				wantRejection = "missing_english_title"
			}
			if !strings.Contains(prompt, wantRejection) || input.ParentDir != "" || counts.primary.Load() != 1 {
				t.Errorf("semantic retry has wrong context, cause or order")
			}
			row = f.second
		default:
			t.Errorf("unexpected AI endpoint %s", r.URL.Path)
			http.Error(w, "fixture", http.StatusBadRequest)
			return
		}
		row.ID, row.RequestID, row.OriginalFile = input.ID, input.RequestID, input.OriginalFile
		body := string(r10bJSON([]ai.RecognizedTitle{row}))
		record(r.URL.Path+" "+prompt, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%s}]}}]}`, r10bJSON(body))
	})
	models.Store(primary, handler)
	models.Store(secondary, handler)

	var initial atomic.Int32
	a.tmdbClient.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := func(body string) (*http.Response, error) {
			record(r.URL.RequestURI(), body)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if counts.semantic.Load() == 0 {
			if counts.primary.Load() != 0 || !strings.HasPrefix(r.URL.Path, "/3/search/") {
				t.Errorf("unexpected pre-semantic TMDB request %s", r.URL.Path)
			}
			initial.Add(1)
			return response(`{"results":[]}`)
		}
		for _, kind := range []string{"movie", "tv"} {
			if r.URL.Path != "/3/search/"+kind {
				continue
			}
			counts.search.Add(1)
			if r.URL.Query().Get("query") != f.second.ENTitle {
				t.Errorf("unexpected search query %q", r.URL.Query().Get("query"))
			}
			results := []map[string]any{}
			for _, work := range f.catalogue {
				if work.id.kind == kind {
					results = append(results, work.payload(work.original))
				}
			}
			return response(string(r10bJSON(map[string]any{"results": results})))
		}
		for _, work := range f.catalogue {
			endpoint := fmt.Sprintf("/3/%s/%d", work.id.kind, work.id.id)
			if r.URL.Path == endpoint+"/alternative_titles" {
				counts.aliases.Add(1)
				field := "titles"
				if work.id.kind == "tv" {
					field = "results"
				}
				return response(string(r10bJSON(map[string]any{field: []map[string]string{{"title": work.alias, "iso_3166_1": "US"}}})))
			}
			if r.URL.Path == endpoint {
				counts.details.Add(1)
				if work.id != f.selected {
					t.Errorf("unexpected typed details lookup %s, want %s", work.id, f.selected)
				}
				body := work.payload(work.localized)
				body["overview"] = "Український опис цієї історії."
				return response(string(r10bJSON(body)))
			}
		}
		t.Errorf("unexpected TMDB request %s", r.URL.Path)
		return nil, fmt.Errorf("unexpected fixture endpoint")
	}))
	scan := a.recognizeAutomaticScan(t.Context(), []string{path})
	rows, err := a.db.GetAllMovies(t.Context())
	if err != nil || len(rows) != 1 || scan.tmdbAccepted != 0 {
		t.Fatalf("expected one persisted AI-queue result, rows=%d scan=%+v err=%v", len(rows), scan, err)
	}
	row := rows[0]
	if row.Filename != a.getFileIdentifier(path) {
		t.Fatalf("unexpected persisted filename %q", row.Filename)
	}
	got := r10bIdentity{row.MediaType, row.TmdbID}
	if got.id == 0 {
		got = r10bIdentity{}
		if row.ReviewReason != "unresolved" || scan.aiAccepted != 0 {
			t.Errorf("invalid unresolved state: review=%q scan=%+v", row.ReviewReason, scan)
		}
	} else if scan.aiAccepted != 1 || row.RecognitionSource != "gemini" || row.RecognitionConfidence != f.second.Confidence {
		t.Errorf("identity was not persisted from the second hypothesis: %+v", row)
	} else if work := f.catalogue[0]; row.TitleEN != work.original || row.TitleUA != work.localized {
		t.Errorf("persisted title evidence = %q / %q, want %q / %q", row.TitleEN, row.TitleUA, work.original, work.localized)
	}
	wireMu.Lock()
	defer wireMu.Unlock()
	return got, r10bMeasure(t, f, row, got, counts, int(initial.Load())), slices.Clone(wire)
}

func r10bMeasure(t *testing.T, f r10bFixture, row storage.Movie, got r10bIdentity, counts *r10bCounts, initial int) r10bResult {
	t.Helper()
	r := r10bResult{initial: initial, search: int(counts.search.Load()), details: int(counts.details.Load()),
		aliases: int(counts.aliases.Load()), primary: int(counts.primary.Load()), semantic: int(counts.semantic.Load())}
	if r.initial == 0 || r.primary != 1 || r.semantic != 1 || r.search != 2 || r.details == 0 {
		t.Errorf("unexpected transport counts: %s", r)
	}
	switch {
	case got.id == 0 && f.oracle.id != 0:
		r.fn++
	case got.id == 0:
		r.unresolved++
	case got == f.oracle:
		r.correct++
	default:
		r.fp++
	}
	if row.NeedsReview {
		r.review++
	}
	return r
}

func (w r10bWork) payload(title string) map[string]any {
	date := fmt.Sprintf("%d-01-01", w.year)
	if w.id.kind == "tv" {
		return map[string]any{"id": w.id.id, "name": title, "original_name": w.original, "first_air_date": date}
	}
	return map[string]any{"id": w.id.id, "title": title, "original_title": w.original, "release_date": date}
}

func r10bJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
