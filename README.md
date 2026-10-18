# shinkrodb

A tool for building and maintaining anime databases by aggregating data from MyAnimeList, AniDB, TVDB, and TMDB.

## Installation

```bash
go install github.com/shinkro/shinkrodb/cmd/shinkrodb@latest
```

Or download from [releases](https://github.com/shinkro/shinkrodb/releases).

## Configuration

Create `$HOME/.config/shinkrodb/config.toml` or use `--config` flag. See `config.toml.example` for reference.

**Required for `run`:**
- `mal_client_id` - MyAnimeList API Client ID (or `SHINKRODB_MAL_CLIENT_ID`)
- `tmdb_api_key` - TMDB API Key (or `SHINKRODB_TMDB_API_KEY`); not required with `--tmdb=skip`

`format` and `genmap` operate on local files and do not require either API key.

**Optional:**
- `discord_webhook_url` - Discord webhook for notifications (or `SHINKRODB_DISCORD_WEBHOOK_URL`)
- `anidb_mode` / `tmdb_mode` - Fetch modes: `default`, `missing`, `all`, or `skip`

## Usage

```bash
# Run full database update
shinkrodb run [--anidb=<mode>] [--tmdb=<mode>] [--root-path=<path>]

# Migrate old HTML cache to SQLite
shinkrodb migrate-cache

# Format mapping files
shinkrodb format [--root-path=<path>]

# Generate mapping files
shinkrodb genmap [--root-path=<path>]

# Show version
shinkrodb version
```

## Output Files

- `malid.json` - MAL IDs with titles and release dates
- `mal-unavailable.json` - Known or contributed candidate IDs that twice returned MAL's JSON `not_found`; retains their metadata and is rechecked on subsequent runs
- `malid-anidbid.json` - Adds AniDB IDs (scraped from MAL)
- `malid-anidbid-tvdbid.json` - Adds TVDB IDs (from anime-lists)
- `malid-anidbid-tvdbid-tmdbid.json` - Adds TMDB IDs (from anime-lists + TMDB API)
- `for-shinkro.json` - Runtime catalogue for shinkro; canonical AniDB TV entries are selected for unambiguous season-1 translation. The complete MAL catalogue remains in the intermediate JSON files and mapping masters; later seasons use TVDB mappings.

## Community mapping files

- `tvdb-mal-master.yaml` contains the MAL catalogue and community TVDB season/episode rules.
- `tmdb-mal-master.yaml` contains all MAL movies, including those with automatic TMDB links. Its nonzero IDs are community mappings; automatic IDs remain in the generated JSON rather than being presented as human-reviewed mappings.
- Existing community mappings survive automatic matches, missing catalogue entries, and movie type changes. Conflicting mappings for the same MAL ID stop regeneration instead of silently selecting one.
- `tvdb-mal-unmapped.yaml` contains entries without a community TVDB mapping. `tmdb-mal-unmapped.yaml` contains current movies without either an automatic or community TMDB mapping.
- `genmap` generates `tvdb-mal.yaml` and `tmdb-mal.yaml` from positive IDs in the masters whose MAL target is in the active `malid.json` catalogue and not in `mal-unavailable.json`. It uses the complete active catalogue, so later-season targets excluded by canonical AniDB runtime selection remain eligible. Local file formats remain compatible with Shinkro.

## MAL catalogue discovery and removals

Each run paginates both `bypopularity` and `all` rankings with `nsfw=true`, combining results by MAL ID. The official API does not provide a documented complete-catalogue export or incremental change feed; a ranking miss alone is not evidence that MAL deleted an entry.

Every previously known ID absent from both discovery passes is checked through `GET /anime/{id}`. Community master IDs also seed discovery, allowing contributions for entries the rankings never exposed. A successful detail response adds or restores the entry to the active catalogue with current metadata. Only two explicit HTTP 404 responses containing MAL's JSON `not_found` classify it as unavailable. Authentication failures, malformed responses, rate limits, server errors, and exhausted network retries fail the run without replacing the active catalogue.

Unavailable entries remain in `mal-unavailable.json` and community masters as history. They are excluded from active JSON output, unmapped work queues, and generated community exports. Restored IDs are checked again and can become active without losing their historical mappings or cached cross-references. Commit the unavailable file together with the generated catalogue and exports.

Requests are sequential, paced to at most two starts per second, and use bounded retries for network failures, HTTP 429, and server errors. Provider `Retry-After` is respected; long requested cooldowns stop the run for a later retry. Discovery refreshes catalogue metadata in bulk and uses per-ID requests only for known omissions, rather than fetching every known title individually.

This is a reconciled catalogue of discovered IDs, not a guarantee that rankings expose every live ID. A bounded numeric ID audit or an independently obtained candidate-ID set is needed to investigate entries never exposed by discovery; confirm those candidates through the official detail endpoint. MAL's public sitemap can provide candidates but is not a reliable current catalogue.

TMDB title searches require a unique matching title and complete release date. Ambiguous results, partial dates, and searches with unexamined pages remain unmapped. Positive cached links are reused; use `--tmdb=all` to recheck existing links. This stricter matching does not retroactively verify old cache entries.

When exactly one TV entry and one ONA entry share an AniDB ID, a unique match to AniDB's main title, with the TV title extending the ONA title, can establish that the ONA owns the link. In that case the TV entry remains in the runtime catalogue with its shared AniDB link and derived TVDB link cleared, preventing shorts from taking over the main series lookup. Only whitespace and case are normalized for this check; the existing TV season selector is unchanged. The complete intermediate catalogue retains the provider links and community mappings retain their rules. Failure to fetch canonical titles stops a run containing these mixed pairs.

Import and cache failures return errors. Malformed mapping files are not recreated as empty masters, and output files are replaced only after a complete write. These replacements are atomic per file, not a transaction across an entire multi-file batch.

## Tests

Run `go test ./...` or `go test -race ./...`. Regression tests use temporary databases, mapping files, and offline provider responses.

## Features

- **Caching**: SQLite cache for efficient re-runs
- **Configurable Fetching**: Control which entries are scraped/fetched
- **Notifications**: Discord webhook support for run completion
- **Statistics**: Comprehensive coverage reports

## Acknowledgments

- **TVDB IDs**: Provided by [Anime-Lists/anime-lists](https://github.com/Anime-Lists/anime-lists)
- **TMDB IDs**: Sourced from [Anime-Lists/anime-lists](https://github.com/Anime-Lists/anime-lists) and the [TMDB API](https://developer.themoviedb.org/reference/)
