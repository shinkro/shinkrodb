package dedupe

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/rs/zerolog"
	"github.com/shinkro/shinkrodb/internal/domain"
	"github.com/shinkro/shinkrodb/internal/testutil"
)

func TestSharedAniDBIDKeepsCanonicalSeasonOneEntry(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/Anime-Lists/anime-lists/raw/master/animetitles.xml" {
			return nil, fmt.Errorf("unexpected request: %s", req.URL.Path)
		}
		return testutil.Response(200, `<animetitles><anime aid="2400"><title type="main">Gantz</title></anime></animetitles>`), nil
	})
	anime := []domain.Anime{
		{MalID: 384, AnidbID: 2400, MainTitle: "Gantz", Type: "tv"},
		{MalID: 395, AnidbID: 2400, MainTitle: "Gantz 2nd Stage", Type: "tv"},
		{MalID: 3, MainTitle: "No AniDB ID", Type: "tv"},
		{MalID: 4, AnidbID: 2400, MainTitle: "Special", Type: "special"},
	}
	original := append([]domain.Anime(nil), anime...)
	count, got, err := NewService(zerolog.Nop(), nil).CheckDupes(context.Background(), anime)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Anime{anime[0], anime[2], anime[3]}
	if count != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical runtime export = %#v, duplicate groups = %d", got, count)
	}
	if !reflect.DeepEqual(anime, original) {
		t.Fatal("canonical export filtering mutated the complete source catalogue")
	}
	tvCount := 0
	for _, row := range got {
		if row.AnidbID == 2400 && row.Type == "tv" {
			tvCount++
		}
	}
	if tvCount != 1 {
		t.Fatalf("AniDB lookup has %d TV targets; want one", tvCount)
	}
}
