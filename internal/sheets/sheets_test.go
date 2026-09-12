package sheets

import (
	"reflect"
	"testing"

	"movielist-app/internal/storage"
)

func TestMovieValues(t *testing.T) {
	values := movieValues([]storage.Movie{{
		Filename: "Series/Season 1/Episode.mkv", TitleUA: "Назва", TitleEN: "Title", Year: "2026",
		VoteAverage: 7.4, VoteCount: 120, Genres: "Drama", Cast: "Actor", Plot: "Plot", PosterURL: "https://example/poster.jpg",
	}})
	if len(values) != 2 || len(values[0]) != 10 {
		t.Fatalf("dimensions = %dx%d", len(values), len(values[0]))
	}
	want := []interface{}{"Series", "Назва", "Title", "2026", 7.4, 120, "Drama", "Actor", "Plot", "https://example/poster.jpg"}
	if !reflect.DeepEqual(values[1], want) {
		t.Fatalf("row = %#v; want %#v", values[1], want)
	}
}

func TestSheetHelpers(t *testing.T) {
	c := &Client{}
	for n, want := range map[int]string{1: "A", 26: "Z", 27: "AA", 52: "AZ", 53: "BA"} {
		if got := c.colLetter(n); got != want {
			t.Errorf("colLetter(%d) = %q; want %q", n, got, want)
		}
	}
	const id = "1AbCdEf"
	if got := c.extractID("https://docs.google.com/spreadsheets/d/" + id + "/edit#gid=0"); got != id {
		t.Fatalf("extractID URL = %q", got)
	}
	if got := c.extractID(id); got != id {
		t.Fatalf("extractID bare ID = %q", got)
	}
}
