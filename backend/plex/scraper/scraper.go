// Package api contains definitions for torrentio
package scraper

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Release struct {
	Source string   `json:"source"`
	Type   string   `json:"type"`
	Title  string   `json:"title"`
	Magnet string   `json:"torrents"`
	Size   float64  `json:"size"`
	Links  []string `json:"links"`
	Seeds  int      `json:"seeds"`
	Path   string
}

// imdb structs
type BehaviorHints struct {
	DefaultVideoID     interface{} `json:"defaultVideoId"`
	HasScheduledVideos bool        `json:"hasScheduledVideos"`
}

type QueryResult struct {
	Query       string  `json:"query"`
	Rank        float64 `json:"rank"`
	CacheMaxAge int     `json:"cacheMaxAge"`
	Metas       []Meta  `json:"metas"`
}

type Meta struct {
	ID            string        `json:"id"`
	IMDBID        string        `json:"imdb_id"`
	Type          string        `json:"type"`
	Name          string        `json:"name"`
	ReleaseInfo   string        `json:"releaseInfo"`
	Poster        string        `json:"poster"`
	Links         []interface{} `json:"links"`
	BehaviorHints BehaviorHints `json:"behaviorHints"`
}

type Data struct {
	Query string `json:"query"`
	//Rank        float64 `json:"rank"`
	CacheMaxAge int    `json:"cacheMaxAge"`
	Metas       []Meta `json:"metas"`
}

// scraper structs
type Streams struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	Name          string               `json:"name"`
	Title         string               `json:"title"`
	InfoHash      string               `json:"infoHash"`
	FileIdx       int                  `json:"fileIdx"`
	BehaviorHints BehaviorHintsScraper `json:"behaviorHints"`
	Sources       []string             `json:"sources"`
}
type BehaviorHintsScraper struct {
	BingeGroup string `json:"bingeGroup"`
}

// top shows struct

type Video struct {
	Name        string `json:"name"`
	Season      int    `json:"season"`
	Number      int    `json:"number"`
	FirstAired  string `json:"firstAired"`
	TVDBID      int    `json:"tvdb_id"`
	Rating      int    `json:"rating"`
	Overview    string `json:"overview"`
	Thumbnail   string `json:"thumbnail"`
	ID          string `json:"id"`
	Released    string `json:"released"`
	Episode     int    `json:"episode"`
	Description string `json:"description"`
}
type SeriesMeta struct {
	ImdbID   string `json:"imdb_id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Released string `json:"released"`
	Year     string `json:"year"`
	Status   string `json:"status"`
	ID       string `json:"id"`
	Videos   []struct {
		Name        string  `json:"name"`
		Season      int     `json:"season"`
		Number      int     `json:"number"`
		FirstAired  string  `json:"firstAired"`
		TvdbID      int     `json:"tvdb_id"`
		Rating      float64 `json:"rating"`
		Overview    string  `json:"overview"`
		Thumbnail   string  `json:"thumbnail"`
		ID          string  `json:"id"`
		Released    string  `json:"released"`
		Episode     int     `json:"episode"`
		Description string  `json:"description"`
	} `json:"videos"`
}

type Popularities struct {
	Trakt      int     `json:"trakt"`
	Stremio    float64 `json:"stremio"`
	StremioLib int     `json:"stremio_lib"`
	Moviedb    float64 `json:"moviedb"`
}

type Trailer struct {
	Source string `json:"source"`
	Type   string `json:"type"`
}

type SeriesQueryResult struct {
	Metas []SeriesMeta `json:"metas"`
}

var default_opts = "https://torrentio.strem.fun/sort=qualitysize|qualityfilter=480p,scr,cam/manifest.json"

func Scrape(query string, contentType string, quality string, season string, episode string, ignoreHDR bool) []*Release {
	scrapedReleases := []*Release{}

	opts := ""
	optsSplit := strings.Split(default_opts, "/")
	if strings.HasSuffix(default_opts, "manifest.json") && len(optsSplit) >= 2 {
		opts = optsSplit[len(optsSplit)-2]
	}

	var url string
	client := &http.Client{
		Timeout: 15 * time.Second,
	}
	var req *http.Request
	var resp *http.Response
	var responseBody []byte
	// fetch imdbid to search for torrent search
	if contentType == "show" {
		url = "https://v3-cinemeta.strem.io/catalog/series/top/search=" + query + ".json"
	} else {
		url = "https://v3-cinemeta.strem.io/catalog/movie/top/search=" + query + ".json"
	}
	req, _ = http.NewRequest("GET", url, nil)
	resp, _ = client.Do(req)

	defer resp.Body.Close()

	responseBody, _ = ioutil.ReadAll(resp.Body)
	var response Data
	var err = json.Unmarshal(responseBody, &response)
	if err != nil {
		fmt.Println("Error:", err)
		return nil
	}

	// set search query
	query = response.Metas[0].IMDBID
	var qualities []string = []string{"4k", "1080p", "720p", "other", "hdrall", "unknown", "cam"}
	opts = "sort=seeders"
	// quality sort
	if quality != "" {
		opts += "&"
		for _, q := range qualities {
			if !(q == quality) {
				opts += q
			}
		}
	}

	var url2 string
	if contentType == "movie" {
		url2 = "https://torrentio.strem.fun/" + opts + "/stream/movie/" + query + ".json"
	}

	if contentType == "show" {
		url2 = "https://torrentio.strem.fun/" + opts + "/stream/series/" + query + ":" + season + ":" + episode + ".json"
	}

	req, _ = http.NewRequest("GET", url2, nil)
	resp, err = client.Do(req)

	fmt.Println(url2)

	if err != nil {
		fmt.Println(err)
	}

	responseBody, _ = ioutil.ReadAll(resp.Body)

	var scraperResponse Streams
	var scraperErr = json.Unmarshal(responseBody, &scraperResponse)
	if scraperErr != nil {
		fmt.Println("Error:", scraperErr)
		return nil
	}

	if err != nil || len(scraperResponse.Streams) == 0 {
		var errMsg string = "[torrentio] error: unknown error"
		fmt.Println(errMsg)
		return scrapedReleases
	}

	for _, result := range scraperResponse.Streams {

		titleSplit := strings.Split(result.Title, "\n")
		title := titleSplit[0]
		title = strings.ReplaceAll(title, " ", ".")

		var size float64
		matchSizeGB := regexp.MustCompile(`💾 ([0-9.]+) GB`).FindStringSubmatch(result.Title)
		if len(matchSizeGB) > 0 {
			size = parseFloat(matchSizeGB[0])
		} else {
			matchSizeMB := regexp.MustCompile(`💾 ([0-9.]+) MB`).FindStringSubmatch(result.Title)
			if len(matchSizeMB) > 0 {
				size = parseFloat(matchSizeMB[0]) / 1000
			} else {
				size = 0
			}
		}

		var magnet string = "magnet:?xt=urn:btih:" + result.InfoHash

		var seeds int
		matchSeeds := regexp.MustCompile(`👤 ([0-9]+)`).FindStringSubmatch(result.Title)
		if len(matchSeeds) > 0 {
			seeds = parseInt(matchSeeds[0])
		}

		source := regexp.MustCompile(`⚙️ (.+)(?:\n|$)`).FindStringSubmatch(result.Title)
		if len(source) == 0 {
			source = []string{"unknown"}
		}

		scrapedReleases = append(scrapedReleases, &Release{
			Source: "[torrentio: " + source[0] + "]",
			Type:   "torrent",
			Title:  title,
			Magnet: magnet,
			Size:   size,
			Seeds:  seeds,
			Path:   strings.Split(result.Title, "\n")[1],
		})
	}

	return scrapedReleases
}

func parseInt(s string) int {
	num, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return num
}

func parseFloat(s string) float64 {
	num, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return num
}

type PlexWatchlistMovies struct {
	LibrarySectionID    string   `xml:"librarySectionID"`
	LibrarySectionTitle string   `xml:"librarySectionTitle"`
	Offset              int      `xml:"offset"`
	TotalSize           int      `xml:"totalSize"`
	Identifier          string   `xml:"identifier"`
	Size                int      `xml:"size"`
	Video               []Movies `xml:"Video"`
}

type Movies struct {
	Art                   string  `xml:"art,attr"`
	Banner                string  `xml:"banner,attr"`
	GUID                  string  `xml:"guid,attr"`
	Key                   string  `xml:"key,attr"`
	PrimaryExtraKey       string  `xml:"primaryExtraKey,attr"`
	Rating                float64 `xml:"rating,attr"`
	RatingKey             string  `xml:"ratingKey,attr"`
	Studio                string  `xml:"studio,attr"`
	Tagline               string  `xml:"tagline,attr"`
	Type                  string  `xml:"type,attr"`
	Thumb                 string  `xml:"thumb,attr"`
	AddedAt               int64   `xml:"addedAt,attr"`
	Duration              int64   `xml:"duration,attr"`
	PublicPagesURL        string  `xml:"publicPagesURL,attr"`
	Slug                  string  `xml:"slug,attr"`
	UserState             int     `xml:"userState,attr"`
	Title                 string  `xml:"title,attr"`
	ContentRating         string  `xml:"contentRating,attr"`
	OriginallyAvailableAt string  `xml:"originallyAvailableAt,attr"`
	Year                  int     `xml:"year,attr"`
	AudienceRating        float64 `xml:"audienceRating,attr"`
	AudienceRatingImage   string  `xml:"audienceRatingImage,attr"`
	RatingImage           string  `xml:"ratingImage,attr"`
	IMDBRatingCount       int     `xml:"imdbRatingCount,attr"`
}

type Image struct {
	Alt  string `xml:"alt,attr"`
	Type string `xml:"type,attr"`
	URL  string `xml:"url,attr"`
}

type PlexItem struct {
	XMLName   xml.Name     `xml:"MediaContainer"`
	Directory PlexItemData `xml:"Directory"`
}

type Guid struct {
	ID string `xml:"id,attr"`
}

type PlexItemData struct {
	Art                   string `xml:"art,attr"`
	Banner                string `xml:"banner,attr"`
	Guid                  string `xml:"guid,attr"`
	Key                   string `xml:"key,attr"`
	PrimaryExtraKey       string `xml:"primaryExtraKey,attr"`
	Rating                string `xml:"rating,attr"`
	RatingKey             string `xml:"ratingKey,attr"`
	Studio                string `xml:"studio,attr"`
	Subtype               string `xml:"subtype,attr"`
	Summary               string `xml:"summary,attr"`
	Type                  string `xml:"type,attr"`
	Thumb                 string `xml:"thumb,attr"`
	AddedAt               string `xml:"addedAt,attr"`
	Duration              string `xml:"duration,attr"`
	PublicPagesURL        string `xml:"publicPagesURL,attr"`
	Slug                  string `xml:"slug,attr"`
	UserState             string `xml:"userState,attr"`
	Title                 string `xml:"title,attr"`
	LeafCount             string `xml:"leafCount,attr"`
	ChildCount            string `xml:"childCount,attr"`
	SkipChildren          string `xml:"skipChildren,attr"`
	IsContinuingSeries    string `xml:"isContinuingSeries,attr"`
	ContentRating         string `xml:"contentRating,attr"`
	OriginallyAvailableAt string `xml:"originallyAvailableAt,attr"`
	Year                  string `xml:"year,attr"`
	RatingImage           string `xml:"ratingImage,attr"`
	IMDBRatingCount       string `xml:"imdbRatingCount,attr"`
	Guids                 []Guid `xml:"Guid"`
}

func getPlexItemByRatingKey(ratingKey string, apiKey string) PlexItem {
	url := "https://metadata.provider.plex.tv/library/metadata/" + ratingKey + "?X-Plex-Token=" + apiKey

	response, err := http.Get(url)

	if err != nil {
		fmt.Println(err)
	}
	var data PlexItem
	if err := xml.NewDecoder(response.Body).Decode(&data); err != nil {
		fmt.Println(err)
	}
	response.Body.Close()
	return data
}

type WebSocketNotificationItem struct {
	RatingKey string `xml:"ratingKey,attr"`
	Key       string `xml:"key,attr"`
}

type WebSocketNotification struct {
	XMLName             xml.Name                    `xml:"MediaContainer"`
	Size                string                      `xml:"size,attr"`
	AllowSync           string                      `xml:"allowSync,attr"`
	Identifier          string                      `xml:"identifier,attr"`
	LibrarySectionID    string                      `xml:"librarySectionID,attr"`
	LibrarySectionTitle string                      `xml:"librarySectionTitle,attr"`
	LibrarySectionUUID  string                      `xml:"librarySectionUUID,attr"`
	MediaTagPrefix      string                      `xml:"mediaTagPrefix,attr"`
	MediaTagVersion     string                      `xml:"mediaTagVersion,attr"`
	Videos              []WebSocketNotificationItem `xml:"Video"`
}

func getPlexItemByRatingKeyLocal(url string, apiKey string, ratingKey string) WebSocketNotificationItem {
	response, err := http.Get(url + ratingKey + "?X-Plex-Token=" + apiKey)

	if err != nil {
		fmt.Println(err)
	}
	var data WebSocketNotification
	if err := xml.NewDecoder(response.Body).Decode(&data); err != nil {
		fmt.Println(err)
	}
	response.Body.Close()
	return data.Videos[0]
}

func GetPlexMovies(apiKey string) []Movies {
	url := "https://metadata.provider.plex.tv/library/sections/watchlist/all?type=1&X-Plex-Token=" + apiKey

	response, err := http.Get(url)

	if err != nil {
		fmt.Println(err)
	}
	var data PlexWatchlistMovies
	if err := xml.NewDecoder(response.Body).Decode(&data); err != nil {
		fmt.Println(err)
	}

	response.Body.Close()
	return data.Video
}

type PlexWatchlistSeries struct {
	XMLName             xml.Name `xml:"MediaContainer"`
	LibrarySectionID    string   `xml:"librarySectionID,attr"`
	LibrarySectionTitle string   `xml:"librarySectionTitle,attr"`
	Offset              string   `xml:"offset,attr"`
	TotalSize           string   `xml:"totalSize,attr"`
	Identifier          string   `xml:"identifier,attr"`
	Size                string   `xml:"size,attr"`
	Directories         []Series `xml:"Directory"`
}

type Series struct {
	Art                   string  `xml:"art,attr"`
	Banner                string  `xml:"banner,attr"`
	GUID                  string  `xml:"guid,attr"`
	Key                   string  `xml:"key,attr"`
	PrimaryExtraKey       string  `xml:"primaryExtraKey,attr"`
	Rating                float64 `xml:"rating,attr"`
	RatingKey             string  `xml:"ratingKey,attr"`
	Studio                string  `xml:"studio,attr"`
	Subtype               string  `xml:"subtype,attr"`
	Type                  string  `xml:"type,attr"`
	Theme                 string  `xml:"theme,attr"`
	Thumb                 string  `xml:"thumb,attr"`
	AddedAt               int64   `xml:"addedAt,attr"`
	Duration              int64   `xml:"duration,attr"`
	PublicPagesURL        string  `xml:"publicPagesURL,attr"`
	Slug                  string  `xml:"slug,attr"`
	UserState             int     `xml:"userState,attr"`
	Title                 string  `xml:"title,attr"`
	LeafCount             int     `xml:"leafCount,attr"`
	ChildCount            int     `xml:"childCount,attr"`
	SkipChildren          int     `xml:"skipChildren,attr"`
	IsContinuingSeries    int     `xml:"isContinuingSeries,attr"`
	ContentRating         string  `xml:"contentRating,attr"`
	OriginallyAvailableAt string  `xml:"originallyAvailableAt,attr"`
	Year                  int     `xml:"year,attr"`
	RatingImage           string  `xml:"ratingImage,attr"`
	IMDBRatingCount       int     `xml:"imdbRatingCount,attr"`
	Guids                 []Guid  `xml:"Guid"`
	Seasons               []TraktSeason
}

func GetPlexShows(apiKey string, traktClientID string) []Series {
	url := "https://metadata.provider.plex.tv/library/sections/watchlist/all?type=2&X-Plex-Token=" + apiKey

	response, err := http.Get(url)

	if err != nil {
		fmt.Println(err)
	}

	var data PlexWatchlistSeries
	if err := xml.NewDecoder(response.Body).Decode(&data); err != nil {
		fmt.Println(err)
	}

	for i := range data.Directories {
		// make 2 more calls, to get data from plex and another one to get data from trakt.tv for seasons & episode count
		plexItem := getPlexItemByRatingKey(data.Directories[i].RatingKey, apiKey)

		seasonData := getSeasonData(strings.Split(plexItem.Directory.Guids[0].ID, "//")[1], traktClientID)

		data.Directories[i].Seasons = append(data.Directories[i].Seasons, seasonData...)
	}

	response.Body.Close()
	return data.Directories
}

type IDStruct struct {
	Trakt  int `json:"trakt"`
	TVDB   int `json:"tvdb"`
	TMDB   int `json:"tmdb"`
	TVRage int `json:"tvrage"`
}

type TraktSeason struct {
	Number        int       `json:"number"`
	IDs           IDStruct  `json:"ids"`
	Rating        float64   `json:"rating"`
	Votes         int       `json:"votes"`
	EpisodeCount  int       `json:"episode_count"`
	AiredEpisodes int       `json:"aired_episodes"`
	Title         string    `json:"title"`
	Overview      string    `json:"overview"`
	FirstAired    time.Time `json:"first_aired"`
	UpdatedAt     time.Time `json:"updated_at"`
	Network       string    `json:"network"`
}

type ListItem struct {
	Rank     int         `json:"rank"`
	ID       int         `json:"id"`
	ListedAt string      `json:"listed_at"`
	Notes    interface{} `json:"notes"`
	Type     string      `json:"type"`
	Show     Show        `json:"show,omitempty"`
	Movie    Movie       `json:"movie,omitempty"`
}

type Show struct {
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Ids     Ids    `json:"ids"`
	Seasons []TraktSeason
}

type Movie struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	Ids   Ids    `json:"ids"`
}

type Ids struct {
	Trakt int    `json:"trakt"`
	Slug  string `json:"slug"`
	Imdb  string `json:"imdb"`
	Tmdb  int    `json:"tmdb"`
}

func getTraktItems(url string, traktClientID string) ([]ListItem, error) {
	client := &http.Client{}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("trakt-api-key", traktClientID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var items []ListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}

	return items, nil
}

func GetTraktMovieListItems(listID string, traktClientID string) ([]ListItem, error) {
	url := fmt.Sprintf("https://api.trakt.tv/lists/%s/items/movies", listID)
	movieItems, err := getTraktItems(url, traktClientID)
	if err != nil {
		return nil, err
	}
	return movieItems, nil
}

func GetTraktShowListItems(listID string, traktClientID string) ([]ListItem, error) {
	url := fmt.Sprintf("https://api.trakt.tv/lists/%s/items/shows", listID)
	showItems, err := getTraktItems(url, traktClientID)
	if err != nil {
		return nil, err
	}

	for i, show := range showItems {
		seasons := getSeasonData(show.Show.Ids.Imdb, traktClientID)
		showItems[i].Show.Seasons = append(showItems[i].Show.Seasons, seasons...)
	}

	return showItems, nil
}

// TO-DO implement an invalid id response
func getSeasonData(showID string, traktClientID string) []TraktSeason {
	url := "https://api.trakt.tv/shows/" + showID + "/seasons?extended=full"

	client := http.Client{}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Add("trakt-api-key", traktClientID)

	res, _ := client.Do(req)

	var data []TraktSeason
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		fmt.Println(err)
	}

	res.Body.Close()

	return data
}

type PlexLibraries struct {
	XMLName   xml.Name      `xml:"MediaContainer"`
	Directory []PlexLibrary `xml:"Directory"`
}

type PlexLibrary struct {
	Key string `xml:"key,attr"`
}

// plex library update service
func getAllPlexLibraries(url string, apiToken string) []PlexLibrary {
	// http://[IP address]:32400/library/sections/?X-Plex-Token=[PlexToken]

	res, err := http.Get(fmt.Sprintf("https://%s/library/sections/?X-Plex-Token=%s", url, apiToken))

	if err != nil {
		fmt.Println(err)
	}

	var data PlexLibraries
	if err := xml.NewDecoder(res.Body).Decode(&data); err != nil {
		fmt.Println(err)
	}

	return data.Directory
}

func refreshLibrary(url string, libraryID string, apiToken string) {
	_, err := http.Get(fmt.Sprintf("https://%s/library/sections/%s/refresh?force=1&X-Plex-Token=%s", url, libraryID, apiToken))

	if err != nil {
		fmt.Println(err)
	}
}

func UpdateAllLibraries(url string, apiToken string) {
	libraries := getAllPlexLibraries(url, apiToken)

	for i := range libraries {
		refreshLibrary(url, libraries[i].Key, apiToken)
	}
}
