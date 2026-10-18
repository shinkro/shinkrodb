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

func TestMixedTVONAUsesProvenCanonicalONAWithoutDroppingTVTitle(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testutil.Response(200, `<animetitles><anime aid="19908"><title type="main">Dark Summoner to Dekite Iru</title></anime></animetitles>`), nil
	})
	original := []domain.Anime{
		{MalID: 63509, AnidbID: 19908, MainTitle: "Dark Summoner to Dekiteiru", Type: "ona"},
		{MalID: 65169, AnidbID: 19908, TvdbID: 474294, MainTitle: "Dark Summoner to Dekiteiru Mini Anime Gekijou", Type: "tv"},
	}
	source := append([]domain.Anime(nil), original...)
	count, got, err := NewService(zerolog.Nop(), nil).CheckDupes(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]domain.Anime(nil), original...)
	want[1].AnidbID = 0
	want[1].TvdbID = 0
	if count != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime=%#v count=%d", got, count)
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatal("mutated complete provider catalogue")
	}
}

func TestMixedTVONADoesNotGuessCanonicalOwner(t *testing.T) {
	for _, mainTitle := range []string{"TV main", "Unrelated title", ""} {
		t.Run(mainTitle, func(t *testing.T) {
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return testutil.Response(200, `<animetitles><anime aid="1"><title type="main">`+mainTitle+`</title></anime></animetitles>`), nil
			})
			rows := []domain.Anime{{MalID: 1, AnidbID: 1, MainTitle: "ONA title", Type: "ona"}, {MalID: 2, AnidbID: 1, TvdbID: 3, MainTitle: "TV main", Type: "tv"}}
			count, got, err := NewService(zerolog.Nop(), nil).CheckDupes(context.Background(), rows)
			if err != nil {
				t.Fatal(err)
			}
			if count != 0 || !reflect.DeepEqual(got, rows) {
				t.Fatalf("guessed owner: %#v", got)
			}
		})
	}
}

func TestMixedTVONAAbortsWhenCanonicalTitlesCannotBeVerified(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) { return testutil.Response(503, `unavailable`), nil })
	rows := []domain.Anime{{MalID: 1, AnidbID: 1, Type: "ona"}, {MalID: 2, AnidbID: 1, Type: "tv"}}
	_, got, err := NewService(zerolog.Nop(), nil).CheckDupes(context.Background(), rows)
	if err == nil || got != nil {
		t.Fatal("published unchecked mixed-type targets")
	}
}

func TestCanonicalONAWithDifferentTVTitleDoesNotOverrideTVAlias(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = testutil.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testutil.Response(200, `<animetitles><anime aid="13514"><title type="main">Pochitto Hatsumei: Pikachin-Kit</title></anime></animetitles>`), nil
	})
	rows := []domain.Anime{{MalID: 36552, AnidbID: 13514, MainTitle: "Pikachin-Kit", Type: "tv"}, {MalID: 36947, AnidbID: 13514, MainTitle: "Pochitto Hatsumei: Pikachin-Kit", Type: "ona"}}
	count, got, err := NewService(zerolog.Nop(), nil).CheckDupes(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || !reflect.DeepEqual(got, rows) {
		t.Fatalf("changed an unproven alias: %#v", got)
	}
}
