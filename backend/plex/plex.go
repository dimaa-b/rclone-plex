// Package premiumizeme provides an interface to the premiumize.me
// object storage system.
package plex

/*
Run of rclone info
stringNeedsEscaping = []rune{
	0x00, 0x0A, 0x0D, 0x22, 0x2F, 0x5C, 0xBF, 0xFE
	0x00, 0x0A, 0x0D, '"',  '/',  '\\', 0xBF, 0xFE
}
maxFileLength = 255
canWriteUnnormalized = true
canReadUnnormalized   = true
canReadRenormalized   = false
canStream = false
*/

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/backend/plex/api"
	"github.com/rclone/rclone/backend/plex/debrid"
	"github.com/rclone/rclone/backend/plex/scraper"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/config/obscure"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/lib/dircache"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/rclone/rclone/lib/oauthutil"
	"github.com/rclone/rclone/lib/pacer"
	"github.com/rclone/rclone/lib/random"
	"github.com/rclone/rclone/lib/rest"
	"golang.org/x/oauth2"

	"github.com/gorilla/websocket"
)

const (
	rcloneClientID              = "658922194"
	rcloneEncryptedClientSecret = "B5YIvQoRIhcpAYs8HYeyjb9gK-ftmZEbqdh_gNfc4RgO9Q"
	minSleep                    = 10 * time.Millisecond
	maxSleep                    = 2 * time.Second
	decayConstant               = 2   // bigger for slower decay, exponential
	rootID                      = "0" // ID of root folder is always this
	rootURL                     = "https://api.real-debrid.com/rest/1.0/"
	realDebrid                  = "TIYBEQUQLFBJ6SPEBJ7RPIZFFFVXDXPY7PS4QHZ3VABYQ7Y5X2YQ"
	Plex_Token                  = ""
)

// Globals
var (
	// Description of how to auth for this app
	oauthConfig = &oauth2.Config{
		Scopes: nil,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://www.premiumize.me/authorize",
			TokenURL: "https://www.premiumize.me/token",
		},
		ClientID:     rcloneClientID,
		ClientSecret: obscure.MustReveal(rcloneEncryptedClientSecret),
		RedirectURL:  oauthutil.RedirectURL,
	}
)

type Season struct {
	SeasonName       string
	SeasonID         int
	NumberOfEpisodes int
	Episodes         []Episode
}

type Episode struct {
	EpisodeID   string
	EpisodeName string
}

type Item struct {
	Name        string
	Year        string
	ID          string
	ContentType string
	Seasons     []Season
}
type Play struct {
	Name string
	Time int64
}

type CachedLink struct {
	ID    string
	Link  string
	Size  int64
	Added time.Time
}

// Trakt.tv cache arrays (updated every 5 mins)
var cachedTraktMovies []Item
var cachedTraktShows []Item

// Plex watchlist cache
var plexMovies []Item
var plexShows []Item

// Open() function is called several times, has a queue system to prevent multiple calls to torrentio & real-debrid
var recentPlays []Play

// Links are found & saved through, to prevent fetching another link during playback and allow for faster replay
var cachedLinks []CachedLink

var qualitiesToCreate []string = []string{"4k", "1080p", "720p"}

var plexTokens []string
var plexURL string
var traktID string
var traktLists []string
var debrid_api string

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "plex",
		Description: "plex.tv",
		Prefix:      "",
		NewFs:       NewFs,
		Config: func(ctx context.Context, name string, m configmap.Mapper, config fs.ConfigIn) (*fs.ConfigOut, error) {
			return oauthutil.ConfigOut("", &oauthutil.Options{OAuth2Config: oauthConfig})
		},
		Options:      append(oauthutil.SharedOptions, []fs.Option{{Name: "plex_url", Help: `Please input your plex url. (ex: localhost:32400)`, Required: true}, {Name: "plex_tokens", Help: `Please input your plex tokens. If including multiple tokens please separate them with a comma. The first token will be considered the main one and used for library update & play services.`, Required: true}, {Name: "trakt_client_id", Help: `Please input your trakt client id.`, Required: true}, {Name: "trakt_lists", Help: `Please input your trakt lists. If including multiple lists please separate them with a comma.`, Required: true}, {Name: "debrid_api", Help: `Please input your Real-Debrid api key.`, Required: true}}...),
		CommandHelp:  []fs.CommandHelp{},
		Aliases:      []string{},
		Hide:         false,
		MetadataInfo: &fs.MetadataInfo{},
	})

}

// Options defines the configuration for this backend
type Options struct {
	PLEX_URL    string               `config:"plex_url"`
	PLEX_TOKENS string               `config:"plex_tokens"`
	TRAKT_ID    string               `config:"trakt_client_id"`
	TRAKT_LISTS string               `config:"trakt_lists"`
	DEBRID_API  string               `config:"debrid_api"`
	Enc         encoder.MultiEncoder `config:"encoding"`
}

// Fs represents a remote cloud storage system
type Fs struct {
	name         string             // name of this remote
	root         string             // the path we are working on
	opt          Options            // parsed options
	features     *fs.Features       // optional features
	srv          *rest.Client       // the connection to the server
	dirCache     *dircache.DirCache // Map of directory path to directory id
	pacer        *fs.Pacer          // pacer for API calls
	tokenRenewer *oauthutil.Renew   // renew the token on expiry
}

// Object describes a file
type Object struct {
	fs          *Fs       // what this object is part of
	remote      string    // The remote path
	hasMetaData bool      // metadata is present and correct
	size        int64     // size of the object
	modTime     time.Time // modification time of the object
	id          string    // ID of the object
	parentID    string    // ID of parent directory
	mimeType    string    // Mime type of object
	url         string    // URL to download file
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return f.root
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return fmt.Sprintf("premiumize.me root '%s'", f.root)
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// parsePath parses a premiumize.me 'url'
func parsePath(path string) (root string) {
	root = strings.Trim(path, "/")
	return
}

// retryErrorCodes is a slice of error codes that we will retry
var retryErrorCodes = []int{
	429, // Too Many Requests.
	500, // Internal Server Error
	502, // Bad Gateway
	503, // Service Unavailable
	504, // Gateway Timeout
	509, // Bandwidth Limit Exceeded
}

// shouldRetry returns a boolean as to whether this resp and err
// deserve to be retried.  It returns the err as a convenience
func shouldRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if fserrors.ContextError(ctx, &err) {
		return false, err
	}
	return fserrors.ShouldRetry(err) || fserrors.ShouldRetryHTTP(resp, retryErrorCodes), err
}

// readMetaDataForPath reads the metadata from the path
func (f *Fs) readMetaDataForPath(ctx context.Context, path string, directoriesOnly bool, filesOnly bool) (info *api.Item, err error) {
	// defer fs.Trace(f, "path=%q", path)("info=%+v, err=%v", &info, &err)
	leaf, directoryID, err := f.dirCache.FindPath(ctx, path, false)
	if err != nil {
		if err == fs.ErrorDirNotFound {
			return nil, fs.ErrorObjectNotFound
		}
		return nil, err
	}

	lcLeaf := strings.ToLower(leaf)
	_, found, err := f.listAll(ctx, directoryID, directoriesOnly, filesOnly, func(item *api.Item) bool {
		if strings.ToLower(item.Name) == lcLeaf {
			info = item
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fs.ErrorObjectNotFound
	}
	return info, nil
}

// errorHandler parses a non 2xx error response into an error
func errorHandler(resp *http.Response) error {
	body, err := rest.ReadBody(resp)
	if err != nil {
		body = nil
	}
	var e = api.Response{
		Message: string(body),
		Status:  fmt.Sprintf("%s (%d)", resp.Status, resp.StatusCode),
	}
	if body != nil {
		_ = json.Unmarshal(body, &e)
	}
	return &e
}

// Return a url.Values with the api key in
func (f *Fs) baseParams() url.Values {
	params := url.Values{}
	return params
}

// NewFs constructs an Fs from the path, container:path
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	// Parse config into Options struct
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	root = parsePath(root)

	var client *http.Client
	client = fshttp.NewClient(ctx)

	f := &Fs{
		name:  name,
		root:  root,
		opt:   *opt,
		srv:   rest.NewClient(client).SetRoot(rootURL),
		pacer: fs.NewPacer(ctx, pacer.NewDefault(pacer.MinSleep(minSleep), pacer.MaxSleep(maxSleep), pacer.DecayConstant(decayConstant))),
	}
	f.features = (&fs.Features{
		CaseInsensitive:         true,
		CanHaveEmptyDirectories: true,
		ReadMimeType:            true,
	}).Fill(ctx, f)
	f.srv.SetErrorHandler(errorHandler)

	// Get rootID
	f.dirCache = dircache.New(root, rootID, f)

	// Find the current root
	// check if the current item is in cache
	err = f.dirCache.FindRoot(ctx, false)
	// if it gives you an error
	if err != nil {
		// Assume it is a file
		newRoot, remote := dircache.SplitPath(root)
		tempF := *f
		tempF.dirCache = dircache.New(newRoot, rootID, &tempF)
		tempF.root = newRoot
		// Make new Fs which is the parent
		err = tempF.dirCache.FindRoot(ctx, false)
		if err != nil {
			// No root so return old f
			return f, nil
		}
		_, err := tempF.newObjectWithInfo(ctx, remote, nil)
		if err != nil {
			if err == fs.ErrorObjectNotFound {
				// File doesn't exist so return old f
				return f, nil
			}
			return nil, err
		}
		f.features.Fill(ctx, &tempF)
		// XXX: update the old f here instead of returning tempF, since
		// `features` were already filled with functions having *f as a receiver.
		// See https://github.com/rclone/rclone/issues/2182
		f.dirCache = tempF.dirCache
		f.root = tempF.root
		// return an error with an fs which points to the parent
		return f, fs.ErrorIsFile
	}

	// set args from options
	plexTokens = strings.Split(f.opt.PLEX_TOKENS, ",")
	plexURL = f.opt.PLEX_URL
	traktID = f.opt.TRAKT_ID
	traktLists = strings.Split(f.opt.TRAKT_LISTS, ",")
	debrid_api = f.opt.DEBRID_API

	var lastCleared = time.Now().Unix()
	/*
		Websocket handler
	*/
	go func() {
		// initiate the handshake for websocket
		wsURL := fmt.Sprintf("ws://%s/:/websockets/notifications?X-Plex-Token=%s", plexURL, plexTokens[0])
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			fmt.Println("WebSocket dial error:", err)
			return
		}
		defer conn.Close()
		type PlaySessionState struct {
			SessionKey       string `json:"sessionKey"`
			ClientIdentifier string `json:"clientIdentifier"`
			Guid             string `json:"guid"`
			RatingKey        string `json:"ratingKey"`
			URL              string `json:"url"`
			Key              string `json:"key"`
			ViewOffset       int    `json:"viewOffset"`
			PlayQueueItemID  int    `json:"playQueueItemID"`
			PlayQueueID      int    `json:"playQueueID"`
			State            string `json:"state"`
			TranscodeSession string `json:"transcodeSession"`
		}

		type NotificationContainer struct {
			Type                         string             `json:"type"`
			Size                         int                `json:"size"`
			PlaySessionStateNotification []PlaySessionState `json:"PlaySessionStateNotification"`
		}

		var IntermediateStruct struct {
			NotificationContainer NotificationContainer `json:"NotificationContainer"`
		}

		for {
			_, messageData, _ := conn.ReadMessage()
			err := json.Unmarshal(messageData, &IntermediateStruct)
			if err != nil {
				print(err)
			}

			type Video struct {
				RatingKey           string `xml:"ratingKey,attr"`
				Key                 string `xml:"key,attr"`
				GUID                string `xml:"guid,attr"`
				Type                string `xml:"type,attr"`
				Title               string `xml:"title,attr"`
				LibrarySectionTitle string `xml:"librarySectionTitle,attr"`
				LibrarySectionID    string `xml:"librarySectionID,attr"`
				LibrarySectionKey   string `xml:"librarySectionKey,attr"`
				Summary             string `xml:"summary,attr"`
				Year                string `xml:"year,attr"`
				Duration            string `xml:"duration,attr"`
				OriginallyAvailable string `xml:"originallyAvailableAt,attr"`
				UpdatedAt           string `xml:"updatedAt,attr"`
			}

			type MediaContainer struct {
				XMLName             xml.Name `xml:"MediaContainer"`
				Size                string   `xml:"size,attr"`
				AllowSync           string   `xml:"allowSync,attr"`
				Identifier          string   `xml:"identifier,attr"`
				LibrarySectionID    string   `xml:"librarySectionID,attr"`
				LibrarySectionTitle string   `xml:"librarySectionTitle,attr"`
				LibrarySectionUUID  string   `xml:"librarySectionUUID,attr"`
				MediaTagPrefix      string   `xml:"mediaTagPrefix,attr"`
				MediaTagVersion     string   `xml:"mediaTagVersion,attr"`
				Videos              []Video  `xml:"Video"`
			}

			if len(IntermediateStruct.NotificationContainer.PlaySessionStateNotification) != 0 && (IntermediateStruct.NotificationContainer.PlaySessionStateNotification[0].State == "playing" || IntermediateStruct.NotificationContainer.PlaySessionStateNotification[0].State == "paused") {
				/*
					Find the item being played by the user, and add it to the recentPlays array
				*/
				key := IntermediateStruct.NotificationContainer.PlaySessionStateNotification[0].Key

				res, err2 := http.Get("http://" + plexURL + key + "?X-Plex-Token=" + plexTokens[0])

				if err2 != nil {
					fmt.Println(err2)
				}

				fmt.Println(res.StatusCode)

				var container MediaContainer
				body, _ := ioutil.ReadAll(res.Body)
				err := xml.Unmarshal(body, &container)

				if err != nil {
					fmt.Println(err)
				}

				fmt.Println("Play started.")
				recentPlays = append(recentPlays, Play{
					Name: strings.ToLower(container.Videos[0].Title),
					Time: time.Now().Unix(),
				})
			}

			// check first item in array and if it's been more than 30 seconds (time is in unix time), remove this object from the array
			if len(recentPlays) > 0 && time.Now().Unix()-recentPlays[0].Time > 30000 {
				recentPlays = recentPlays[1:]
			}

			if time.Now().Unix()-lastCleared > 5000 {
				for i := range linkFetchQueue {
					if time.Now().Unix()-linkFetchQueue[i].timeAdded > 15 {
						linkFetchQueue = append(linkFetchQueue[:i], linkFetchQueue[i+1:]...)
					}
				}
				lastCleared = time.Now().Unix()
			}
		}
	}()

	// Trakt.tv List Handler
	var lastUpdated = time.Now().Unix() - 60*5
	go func() {
		// trakt lists
		for {
			if time.Now().Unix()-lastUpdated > 60*5 {
				fmt.Println("[Trakt.tv List Update] Update started.")
				cachedTraktShows = nil
				cachedTraktMovies = nil
				for _, id := range traktLists {
					// shows
					traktShows, err := scraper.GetTraktShowListItems(id, traktID)

					if err != nil {
						fmt.Println(err)
					}

					for i := range traktShows {
						item := Item{
							Name: traktShows[i].Show.Title,
							Year: strconv.Itoa(traktShows[i].Show.Year),
						}

						id := strings.ToLower(traktShows[i].Show.Title) + "/" + strconv.Itoa(traktShows[i].Show.Year) + "/show/"
						for _, season := range traktShows[i].Show.Seasons {
							if season.Number == 0 {
								continue
							}
							id = id + strconv.Itoa(season.Number) + "_" + strconv.Itoa(season.AiredEpisodes) + "_" + "|"
						}

						item.ID = id

						cachedTraktShows = append(cachedTraktShows, item)
					}

					// movies
					traktMovies, err := scraper.GetTraktMovieListItems(id, traktID)

					if err != nil {
						fmt.Println(err)
					}

					for i := range traktMovies {
						item := Item{
							Name: traktMovies[i].Movie.Title,
							Year: strconv.Itoa(traktMovies[i].Movie.Year),
						}

						id := strings.ToLower(traktMovies[i].Movie.Title) + "/" + strconv.Itoa(traktMovies[i].Movie.Year) + "/movie"
						item.ID = id

						cachedTraktMovies = append(cachedTraktMovies, item)
					}
				}

				lastUpdated = time.Now().Unix()
				fmt.Println("[Trakt.tv List Update] Update finished.")
			}
		}
	}()

	/*
		Plex watchlist update

		If an item is added to watchlist, removed (size changed or first / last item is modified), cause a library refresh in order to add the new item

		In order to reduce calls, add items here to cache to prevent multiple calls
	*/

	var plexLastChecked = time.Now().Unix()
	go func() {
		for {
			if time.Now().Unix()-plexLastChecked > 5 {
				// TO-DO check if the watchlist was updated

				previousPlexMovies := plexMovies
				previousPlexShows := plexShows

				plexMovies = nil
				plexShows = nil

				// get all users watchlists
				for _, token := range plexTokens {

					// movies
					plex_watchlist := scraper.GetPlexMovies(token)

					for i := range plex_watchlist {
						plexMovies = append(plexMovies, Item{
							Name: plex_watchlist[i].Title,
							Year: strconv.Itoa(plex_watchlist[i].Year),
							ID:   strings.ToLower(plex_watchlist[i].Title) + "/" + strconv.Itoa(plex_watchlist[i].Year) + "/movie",
						})
					}

					// shows
					plex_watchlist_shows := scraper.GetPlexShows(token, traktID)

					for i := range plex_watchlist_shows {
						id := strings.ToLower(plex_watchlist_shows[i].Title) + "/" + strconv.Itoa(plex_watchlist_shows[i].Year) + "/show/"
						for _, season := range plex_watchlist_shows[i].Seasons {
							if season.Number == 0 {
								continue
							}
							id = id + strconv.Itoa(season.Number) + "_" + strconv.Itoa(season.AiredEpisodes) + "_" + "|"
						}

						plexShows = append(plexShows, Item{
							Name: plex_watchlist_shows[i].Title,
							ID:   id,
						})
					}
					// check to see if the watchlist was different from the previous, and if it was force a rescan from scraper
					if (len(plex_watchlist) != len(previousPlexMovies) || len(plex_watchlist_shows) != len(previousPlexShows)) && len(previousPlexMovies) != 0 && len(previousPlexShows) != 0 {
						scraper.UpdateAllLibraries(plexURL, plexTokens[0])
					}

					plexLastChecked = time.Now().Unix()
				}
			}

		}
	}()

	return f, nil
}

// Return an Object from a path
//
// If it can't be found it returns the error fs.ErrorObjectNotFound.
func (f *Fs) newObjectWithInfo(ctx context.Context, remote string, info *api.Item) (fs.Object, error) {
	o := &Object{
		fs:     f,
		remote: remote,
	}
	var err error
	if info != nil {
		// Set info
		err = o.setMetaData(info)
	} else {
		err = o.readMetaData(ctx) // reads info and meta, returning an error
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// NewObject finds the Object at remote.  If it can't be found
// it returns the error fs.ErrorObjectNotFound.
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	return f.newObjectWithInfo(ctx, remote, nil)
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(ctx context.Context, pathID, leaf string) (pathIDOut string, found bool, err error) {
	// Find the leaf in pathID
	var newDirID string
	newDirID, found, err = f.listAll(ctx, pathID, true, false, func(item *api.Item) bool {
		if strings.EqualFold(item.Name, leaf) {
			pathIDOut = item.ID
			return true
		}
		return false
	})
	// Update the Root directory ID to its actual value
	if pathID == rootID {
		f.dirCache.SetRootIDAlias(newDirID)
	}
	return pathIDOut, found, err
}

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(ctx context.Context, pathID, leaf string) (newID string, err error) {
	return "", nil
}

// list the objects into the function supplied
//
// If directories is set it only sends directories
// User function to process a File item from listAll
//
// Should return true to finish processing
type listAllFn func(*api.Item) bool

// Lists the directory required calling the user function on each item found
//
// If the user fn ever returns true then it early exits with found = true
//
// It returns a newDirID which is what the system returned as the directory ID
func (f *Fs) listAll(ctx context.Context, dirID string, directoriesOnly bool, filesOnly bool, fn listAllFn) (newDirID string, found bool, err error) {
	var result []api.Item
	if dirID == rootID {
		// create movie / tv show folders
		result = append(result, api.Item{
			Name: "Movies",
			ID:   "movies",
			Type: "folder",
		})
		result = append(result, api.Item{
			Name: "TV Shows",
			ID:   "shows",
			Type: "folder",
		})
	} else if dirID == "shows" {
		//the office/2005/show/1_5_2_5_

		fmt.Println(plexShows)
		for i := range plexShows {
			result = append(result, api.Item{
				Name: plexShows[i].Name,
				ID:   plexShows[i].ID,
				Type: "folder",
			})
		}
		for i := range cachedTraktShows {
			result = append(result, api.Item{
				Name: cachedTraktShows[i].Name,
				ID:   cachedTraktShows[i].ID,
				Type: "folder",
			})
		}

		/*
			plexShows := scraper.GetPlexShows(plexAPI)

			for i := range plexShows {
				id := strings.ToLower(plexShows[i].Title) + "/" + strconv.Itoa(plexShows[i].Year) + "/show/"
				for _, season := range plexShows[i].Seasons {
					if season.Number == 0 {
						continue
					}
					id = id + strconv.Itoa(season.Number) + "_" + strconv.Itoa(season.AiredEpisodes) + "_" + "|"
				}

				result = append(result, api.Item{
					Name: plexShows[i].Title,
					ID:   id,
					Type: "folder",
				})
			}
		*/

	} else if dirID == "movies" {
		// TO-DO create a cache system & lookup system for movies to create files on demand

		// temporary system to display several movies

		/*
			for i := range plexMovies {
				result = append(result, api.Item{
					Name: plexMovies[i].Name + " (" + plexMovies[i].Year + ")",
					ID:   plexMovies[i].ID,
				})
			}
		*/

		//plex_watchlist := scraper.GetPlexMovies(plexAPI)

		for i := range plexMovies {
			result = append(result, api.Item{
				Name: plexMovies[i].Name + " (" + plexMovies[i].Year + ")",
				ID:   plexMovies[i].ID,
			})
		}

		for i := range cachedTraktMovies {
			result = append(result, api.Item{
				Name: cachedTraktMovies[i].Name + " (" + cachedTraktMovies[i].Year + ")",
				ID:   cachedTraktMovies[i].ID,
			})
		}

	} else {
		// this means it's a movie or show folder, in this code differentiate between them and create the correct objects
		var ContentTitle = dirID
		var objectData = strings.Split(ContentTitle, "/")
		var contentType = objectData[2]

		// TO-DO rewrite system to fetch link from dirID

		// Append the movie file from the folder data
		if contentType == "movie" {
			/*
				for _, quality := range qualitiesToCreate {
					movieObj := api.Item{
						Name: objectData[0] + " (" + objectData[1] + ") [" + quality + "].mkv",
						ID:   objectData[0] + "/nil/movie/" + quality,
						Size: 5000,
					}
					result = append(result, movieObj)
				}
			*/

			movieObj := api.Item{
				Name: objectData[0] + " (" + objectData[1] + ").mp4",
				ID:   objectData[0] + "/nil/movie/",
				Size: 5000,
			}
			result = append(result, movieObj)
		} else if contentType == "show" {
			data := strings.Split(dirID, "/")
			episodeData := strings.Split(data[3], "|")

			for i := 0; i < len(episodeData)-1; i++ {
				currentData := strings.Split(episodeData[i], "_")
				seasonID := currentData[0]
				episodeCount := currentData[1]

				result = append(result, api.Item{
					Name: "Season " + seasonID,
					ID:   objectData[0] + "/" + objectData[1] + "/season/" + seasonID + "_" + episodeCount,
					Type: "folder",
				})
			}

			//the office/2005/show/1_5_2_5_
			//the office/2005/season/1_5
		} else if contentType == "season" {
			// Seasons objects only have current season & num of episodes in their id.
			data := strings.Split(dirID, "/")
			showTitle := data[0]
			showData := strings.Split(data[3], "_")

			numOfEpisodes, _ := strconv.Atoi(showData[1])
			//episodeName := ""
			for i := 0; i < numOfEpisodes+1; i++ {
				/*
					for _, quality := range qualitiesToCreate {
						result = append(result, api.Item{
							Name: "S" + showData[0] + "E" + strconv.Itoa(i) + "[" + quality + "].mkv",
							ID:   showTitle + "/nil/episode/" + showData[0] + "/" + strconv.Itoa(i) + "/" + quality,
							Size: 5000,
							Type: "file",
						})
					}
				*/
				result = append(result, api.Item{
					Name: "S" + showData[0] + "E" + strconv.Itoa(i) + ".mp4",
					ID:   showTitle + "/nil/episode/" + showData[0] + "/" + strconv.Itoa(i) + "/",
					Size: 5000,
					Type: "file",
				})
			}
			/*
				for i := range cached {
					if strings.ToLower(cached[i].Name) == objectData[0] {
						// found show object, search for season
						for _, season := range cached[i].Seasons {

							if strings.ToLower(season.SeasonName) == objectData[3] {

								// found the right season - append all of the episodes
								for _, episode := range season.Episodes {
									result = append(result, api.Item{
										Name: "S" + season.SeasonID + "E" + episode.EpisodeID + " " + episode.EpisodeName + ".mkv",
										ID:   showTitle + "/nil/episode/" + season.SeasonID + "/" + episode.EpisodeID,
										Link: showTitle + "/nil/episode/" + season.SeasonID + "/" + episode.EpisodeID,
										Size: 5000,
									})
								}
							}
						}
					}
				}

			*/
		}
	}

	// add missing data onto the objects
	for i := range result {
		item := &result[i]
		item.Link = item.ID
		// if the item type isn't explicitly set, use the base cases
		if item.Type == "" {
			if dirID == rootID || dirID == "shows" || dirID == "movies" || dirID == "default" {
				// if the item is in root (meaning only folders, movies and shows) or in shows or movies,
				// the files created must be folders, otherwise, create the objects as files
				item.Type = "folder"
			} else {
				item.Type = "file"
			}
		}
		if item.Type == api.ItemTypeFolder {
			if filesOnly {
				continue
			}
		} else if item.Type == api.ItemTypeFile {
			if directoriesOnly {
				continue
			}
		} else {
			fs.Debugf(f, "Ignoring %q - unknown type %q", item.Name, item.Type)
			continue
		}
		item.Name = f.opt.Enc.ToStandardName(item.Name)
		if fn(item) {
			found = true
			break
		}
	}

	// if not the main sorting folders, return data from either show or movie (serve the single movie file)
	// TO-DO implement show season system
	return
}

// List the objects and directories in dir into entries.  The
// entries can be returned in any order but should be for a
// complete directory.
//
// dir should be "" to list the root, and should not have
// trailing slashes.
//
// This should return ErrDirNotFound if the directory isn't
// found.
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	directoryID, err := f.dirCache.FindDir(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	var iErr error
	_, _, err = f.listAll(ctx, directoryID, false, false, func(info *api.Item) bool {
		remote := path.Join(dir, info.Name)
		if info.Type == api.ItemTypeFolder {
			// cache the directory ID for later lookups
			f.dirCache.Put(remote, info.ID)
			d := fs.NewDir(remote, time.Unix(info.CreatedAt, 0)).SetID(info.ID)
			entries = append(entries, d)
		} else if info.Type == api.ItemTypeFile {
			o, err := f.newObjectWithInfo(ctx, remote, info)
			if err != nil {
				iErr = err
				return true
			}
			entries = append(entries, o)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if iErr != nil {
		return nil, iErr
	}
	return entries, nil
}

// Creates from the parameters passed in a half finished Object which
// must have setMetaData called on it
//
// Returns the object, leaf, directoryID and error.
//
// Used to create new objects
func (f *Fs) createObject(ctx context.Context, remote string, modTime time.Time, size int64) (o *Object, leaf string, directoryID string, err error) {
	// Create the directory for the object if it doesn't exist
	leaf, directoryID, err = f.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return
	}
	// Temporary Object under construction
	o = &Object{
		fs:     f,
		remote: remote,
	}
	return o, leaf, directoryID, nil
}

// Put the object
//
// Copy the reader in to the new object which is returned.
//
// The new object may have been created if an error is returned
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	existingObj, err := f.newObjectWithInfo(ctx, src.Remote(), nil)
	switch err {
	case nil:
		return existingObj, existingObj.Update(ctx, in, src, options...)
	case fs.ErrorObjectNotFound:
		// Not found so create it
		return f.PutUnchecked(ctx, in, src, options...)
	default:
		return nil, err
	}
}

// PutUnchecked the object into the container
//
// This will produce an error if the object already exists.
//
// Copy the reader in to the new object which is returned.
//
// The new object may have been created if an error is returned
func (f *Fs) PutUnchecked(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	remote := src.Remote()
	size := src.Size()
	modTime := src.ModTime(ctx)

	o, _, _, err := f.createObject(ctx, remote, modTime, size)
	if err != nil {
		return nil, err
	}
	return o, o.Update(ctx, in, src, options...)
}

// Mkdir creates the container if it doesn't exist
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	_, err := f.dirCache.FindDir(ctx, dir, true)
	return err
}

// purgeCheck removes the root directory, if check is set then it
// refuses to do so if it has anything in
func (f *Fs) purgeCheck(ctx context.Context, dir string, check bool) error {
	root := path.Join(f.root, dir)
	if root == "" {
		return errors.New("can't purge root directory")
	}
	dc := f.dirCache
	rootID, err := dc.FindDir(ctx, dir, false)
	if err != nil {
		return err
	}

	// need to check if empty as it will delete recursively by default
	if check {
		_, found, err := f.listAll(ctx, rootID, false, false, func(item *api.Item) bool {
			return true
		})
		if err != nil {
			return fmt.Errorf("purgeCheck: %w", err)
		}
		if found {
			return fs.ErrorDirectoryNotEmpty
		}
	}

	opts := rest.Opts{
		Method: "POST",
		Path:   "/folder/delete",
		MultipartParams: url.Values{
			"id": {rootID},
		},
		Parameters: f.baseParams(),
	}
	var resp *http.Response
	var result api.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.srv.CallJSON(ctx, &opts, nil, &result)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("rmdir failed: %w", err)
	}
	if err = result.AsErr(); err != nil {
		return fmt.Errorf("rmdir: %w", err)
	}
	f.dirCache.FlushDir(dir)
	if err != nil {
		return err
	}
	return nil
}

// Rmdir deletes the root folder
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	return f.purgeCheck(ctx, dir, true)
}

// Precision return the precision of this Fs
func (f *Fs) Precision() time.Duration {
	return fs.ModTimeNotSupported
}

// Purge deletes all the files in the directory
//
// Optional interface: Only implement this if you have a way of
// deleting all the files quicker than just running Remove() on the
// result of List()
func (f *Fs) Purge(ctx context.Context, dir string) error {
	return f.purgeCheck(ctx, dir, false)
}

// move a file or folder
//
// This is complicated by the fact that there is an API to move files
// between directories and a separate one to rename them.  We try to
// call the minimum number of API calls.
func (f *Fs) move(ctx context.Context, isFile bool, id, oldLeaf, newLeaf, oldDirectoryID, newDirectoryID string) (err error) {
	newLeaf = f.opt.Enc.FromStandardName(newLeaf)
	oldLeaf = f.opt.Enc.FromStandardName(oldLeaf)
	doRenameLeaf := oldLeaf != newLeaf
	doMove := oldDirectoryID != newDirectoryID

	// Now rename the leaf to a temporary name if we are moving to
	// another directory to make sure we don't overwrite something
	// in the destination directory by accident
	if doRenameLeaf && doMove {
		tmpLeaf := newLeaf + "." + random.String(8)
		err = f.renameLeaf(ctx, isFile, id, tmpLeaf)
		if err != nil {
			return fmt.Errorf("Move rename leaf: %w", err)
		}
	}

	// Move the object to a new directory (with the existing name)
	// if required
	if doMove {
		opts := rest.Opts{
			Method:     "POST",
			Path:       "/folder/paste",
			Parameters: f.baseParams(),
			MultipartParams: url.Values{
				"id": {newDirectoryID},
			},
		}
		opts.MultipartParams.Set("items[0][id]", id)
		if isFile {
			opts.MultipartParams.Set("items[0][type]", "file")
		} else {
			opts.MultipartParams.Set("items[0][type]", "folder")
		}
		//replacedLeaf := enc.FromStandardName(leaf)
		var resp *http.Response
		var result api.Response
		err = f.pacer.Call(func() (bool, error) {
			resp, err = f.srv.CallJSON(ctx, &opts, nil, &result)
			return shouldRetry(ctx, resp, err)
		})
		if err != nil {
			return fmt.Errorf("Move http: %w", err)
		}
		if err = result.AsErr(); err != nil {
			return fmt.Errorf("Move: %w", err)
		}
	}

	// Rename the leaf to its final name if required
	if doRenameLeaf {
		err = f.renameLeaf(ctx, isFile, id, newLeaf)
		if err != nil {
			return fmt.Errorf("Move rename leaf: %w", err)
		}
	}

	return nil
}

// Move src to this remote using server-side move operations.
//
// This is stored with the remote path given.
//
// It returns the destination Object and a possible error.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantMove
func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	srcObj, ok := src.(*Object)
	if !ok {
		fs.Debugf(src, "Can't move - not same remote type")
		return nil, fs.ErrorCantMove
	}

	// Create temporary object
	dstObj, leaf, directoryID, err := f.createObject(ctx, remote, srcObj.modTime, srcObj.size)
	if err != nil {
		return nil, err
	}

	// Do the move
	err = f.move(ctx, true, srcObj.id, path.Base(srcObj.remote), leaf, srcObj.parentID, directoryID)
	if err != nil {
		return nil, err
	}

	err = dstObj.readMetaData(ctx)
	if err != nil {
		return nil, err
	}
	return dstObj, nil
}

// DirMove moves src, srcRemote to this remote at dstRemote
// using server-side move operations.
//
// Will only be called if src.Fs().Name() == f.Name()
//
// If it isn't possible then return fs.ErrorCantDirMove
//
// If destination exists then return fs.ErrorDirExists
func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	srcFs, ok := src.(*Fs)
	if !ok {
		fs.Debugf(srcFs, "Can't move directory - not same remote type")
		return fs.ErrorCantDirMove
	}

	srcID, srcDirectoryID, srcLeaf, dstDirectoryID, dstLeaf, err := f.dirCache.DirMove(ctx, srcFs.dirCache, srcFs.root, srcRemote, f.root, dstRemote)
	if err != nil {
		return err
	}

	// Do the move
	err = f.move(ctx, false, srcID, srcLeaf, dstLeaf, srcDirectoryID, dstDirectoryID)
	if err != nil {
		return err
	}
	srcFs.dirCache.FlushDir(srcRemote)
	return nil
}

// PublicLink adds a "readable by anyone with link" permission on the given file or folder.
func (f *Fs) PublicLink(ctx context.Context, remote string, expire fs.Duration, unlink bool) (string, error) {
	_, err := f.dirCache.FindDir(ctx, remote, false)
	if err == nil {
		return "", fs.ErrorCantShareDirectories
	}
	o, err := f.NewObject(ctx, remote)
	if err != nil {
		return "", err
	}
	return o.(*Object).url, nil
}

// About gets quota information
func (f *Fs) About(ctx context.Context) (usage *fs.Usage, err error) {
	var resp *http.Response
	var info api.AccountInfoResponse
	opts := rest.Opts{
		Method:     "POST",
		Path:       "/account/info",
		Parameters: f.baseParams(),
	}
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.srv.CallJSON(ctx, &opts, nil, &info)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	if err = info.AsErr(); err != nil {
		return nil, err
	}
	usage = &fs.Usage{
		Used: fs.NewUsageValue(int64(info.SpaceUsed)),
	}
	return usage, nil
}

// DirCacheFlush resets the directory cache - used in testing as an
// optional interface
func (f *Fs) DirCacheFlush() {
	f.dirCache.ResetRoot()
}

// Hashes returns the supported hash sets.
func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.None)
}

// ------------------------------------------------------------

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info {
	return o.fs
}

// Return a string version
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string {
	return o.remote
}

// Hash returns the SHA-1 of an object returning a lowercase hex string
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 {
	err := o.readMetaData(context.TODO())
	if err != nil {
		fs.Logf(o, "Failed to read metadata: %v", err)
		return 0
	}
	return o.size
}

// setMetaData sets the metadata from info
func (o *Object) setMetaData(info *api.Item) (err error) {
	if info.Type != "file" {
		return fmt.Errorf("%q is %q: %w", o.remote, info.Type, fs.ErrorNotAFile)
	}
	o.hasMetaData = true
	o.size = info.Size
	o.modTime = time.Unix(info.CreatedAt, 0)
	o.id = info.ID
	o.mimeType = info.MimeType
	o.url = info.Link
	return nil
}

// readMetaData gets the metadata if it hasn't already been fetched
//
// it also sets the info
func (o *Object) readMetaData(ctx context.Context) (err error) {
	if o.hasMetaData {
		return nil
	}
	info, err := o.fs.readMetaDataForPath(ctx, o.remote, false, true)
	if err != nil {
		return err
	}
	return o.setMetaData(info)
}

// ModTime returns the modification time of the object
//
// It attempts to read the objects mtime and if that isn't present the
// LastModified returned in the http headers
func (o *Object) ModTime(ctx context.Context) time.Time {
	err := o.readMetaData(ctx)
	if err != nil {
		fs.Logf(o, "Failed to read metadata: %v", err)
		return time.Now()
	}
	return o.modTime
}

// SetModTime sets the modification time of the local fs object
func (o *Object) SetModTime(ctx context.Context, modTime time.Time) error {
	return fs.ErrorCantSetModTime
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool {
	return true
}

type linkQueue struct {
	id        string // name of this remote
	link      string // the path we are working on
	size      int64
	timeAdded int64
}

var linkFetchQueue []linkQueue
var timePassedOverall int64

// Open an object for read
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (in io.ReadCloser, err error) {
	/*
		When file is opened, the function checks if plex has sent a play event notification within a certain timeframe.
		If it has, the file is considered valid and a link search will begin.
		If the file already contains a URL, there will be no need for this search. A file could have a cached url when it was searched once.

		Multiple calls to open() happen during this so there is a link queue, which allows for following calls to get the url
		without calling to the scraper & debrid.

		o.url will contain the id of the object is url isn't present


	*/

	// Check if link has been played recently
	if !isLink(o.url) {
		for _, cachedLink := range cachedLinks {
			if cachedLink.ID == o.url && cachedLink.Added.Unix()-time.Now().Unix() < 86400 {
				fmt.Println(cachedLink.Link)
				o.url = cachedLink.Link
				o.size = cachedLink.Size
			}
		}
	}

	//Check if the object is in the link queue
	if !isLink(o.url) {
		startTime := time.Now().Unix
		for i := range linkFetchQueue {
			if linkFetchQueue[i].id == o.url {
				// wait for completion of the event
				for len(linkFetchQueue) > 0 && linkFetchQueue[i].link == "" {
					// check if the object was cleared from the array
					if linkFetchQueue[i].id != o.url {
						return nil, nil
					}
					if time.Now().Unix()-startTime() > 60 {
						return nil, nil
					}
				}
				o.url = linkFetchQueue[i].link
				o.size = linkFetchQueue[i].size
				break
			}
		}
	}

	if !isLink(o.url) {
		fmt.Println("Link is empty, checking if valid.")
		valid := false
		timePassed := 0
		for !valid && timePassed < 1000 {
			for i := range recentPlays {
				if time.Since(time.Unix(recentPlays[i].Time, 0)).Milliseconds() < 50000 {
					valid = true
					break
				}
			}
			timePassed++
		}
		if !valid {
			fmt.Println("Play deemed invalid.")
			return nil, nil
		}
		/*
			Searching for torrent link & adding it to debrid
		*/
		fmt.Println("Added to linkfetchqueue")
		linkFetchQueue = append(linkFetchQueue, linkQueue{
			id:        o.url,
			link:      "",
			timeAdded: time.Now().Unix(),
		})
		// the amount of elements could change during the run
		queuePosition := len(linkFetchQueue)

		var link string
		data := strings.Split(o.url, "/")
		contentType := data[2]
		contentName := data[0]

		var cachedLink CachedLink

		if contentType == "episode" {
			season := data[3]
			episode := data[4]
			quality := data[5]
			data := scraper.Scrape(contentName, "show", quality, season, episode, true)
			var selectedTorrent = data[0]
			// torrent should be checked too
			torrentInfo, success := debrid.AddToDebrid(data[0].Magnet, debrid_api)

			if !success {
				//linkFetchQueue = append(linkFetchQueue[:queuePosition], linkFetchQueue[queuePosition+1:]...)
				return nil, nil
			}

			/*
				The link is matched by Torrentio
			*/
			var unrestrictedLink string
			for i := range torrentInfo.Files {
				if strings.Contains(torrentInfo.Files[i].Path, selectedTorrent.Path) {
					unrestrictedLink, _ = debrid.UnrestrictLink(torrentInfo.Links[i], debrid_api)
					if unrestrictedLink == "" {
						fmt.Println("Error occured during debrid.UnrestrictLink")
						continue
					}
					break
				}
			}

			if unrestrictedLink == "" {
				// no link was found
				//linkFetchQueue = append(linkFetchQueue[:queuePosition], linkFetchQueue[queuePosition+1:]...)
				return nil, nil
			}

			cachedLink = CachedLink{
				ID: o.url,
			}

			link = unrestrictedLink
			// Episodes usually come in season packs, extract the data from the season and match to the corrent episode
		} else if contentType == "movie" {
			// TO-DO improve matching instead of selecting the first one (to be done after episode matching)

			/*
				Scraper returns incorrectly imdb and therefore the wrong movie (no streams potentially)
				Invalid magnets also also sometimes added?
			*/
			quality := data[3]
			data := scraper.Scrape(contentName, "movie", quality, "", "", false)
			torrentInfo, success := debrid.AddToDebrid(data[0].Magnet, debrid_api)
			debridLinks := torrentInfo.Links
			var unrestrictedLinks []string
			for i := range debridLinks {
				fmt.Println(debridLinks)
				var unrestrictedLink, _ = debrid.UnrestrictLink(debridLinks[i], debrid_api)
				if unrestrictedLink == "" {
					fmt.Println("Error occured during debrid.UnrestrictLink")
					continue
				}
				unrestrictedLinks = append(unrestrictedLinks, unrestrictedLink)
			}

			link = unrestrictedLinks[0]

			cachedLink = CachedLink{
				ID: o.url,
			}

			// update the link in cached
			if len(debridLinks) == 0 {
				// error
				//linkFetchQueue = append(linkFetchQueue[:queuePosition], linkFetchQueue[queuePosition+1:]...)
				return nil, nil
			}

			if !success {
				//linkFetchQueue = append(linkFetchQueue[:queuePosition], linkFetchQueue[queuePosition+1:]...)
				return nil, nil
			}
		}

		/*
			Update the link object here & in listAll
			Updating link might not be necessary, unless fs is updated and the link is reset.
		*/

		o.url = link
		var size, _ = o.fs.getFileSize(ctx, o.url)
		o.size = size
		linkFetchQueue[queuePosition-1].link = o.url
		linkFetchQueue[queuePosition-1].size = o.size
		timePassedOverall = time.Now().Unix()

		cachedLink.Link = o.url
		cachedLink.Size = o.size
		cachedLink.Added = time.Now()
		cachedLinks = append(cachedLinks, cachedLink)
	}

	if o.url == "" {
		return nil, errors.New("can't download - no URL")
	}

	fs.FixRangeOption(options, o.size)
	var resp *http.Response
	opts := rest.Opts{
		Path:    "",
		RootURL: o.url,
		Method:  "GET",
		Options: options,
	}
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, err
}

func (f *Fs) getFileSize(ctx context.Context, link string) (int64, error) {
	var fileSize int64
	var resp *http.Response
	var err error

	opts := rest.Opts{
		Path:    "",
		RootURL: link,
		Method:  "HEAD",
	}

	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.srv.Call(ctx, &opts)
		return shouldRetry(ctx, resp, err)
	})

	if err != nil {
		return 0, err
	}

	fileSize = resp.ContentLength
	if fileSize < 0 {
		return 0, errors.New("Content-Length header not found or invalid")
	}

	return fileSize, nil
}

func isLink(input string) bool {
	parsedURL, err := url.Parse(input)
	if err != nil {
		return false
	}

	// Check if the scheme (protocol) is either http or https
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return false
	}

	// Check if the Host (domain) is present
	if parsedURL.Host == "" {
		return false
	}

	return true
}

// Update the object with the contents of the io.Reader, modTime and size
//
// If existing is set then it updates the object rather than creating a new one.
//
// The new object may have been created if an error is returned
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (err error) {
	remote := o.Remote()
	size := src.Size()

	// Create the directory for the object if it doesn't exist
	leaf, directoryID, err := o.fs.dirCache.FindPath(ctx, remote, true)
	if err != nil {
		return err
	}
	leaf = o.fs.opt.Enc.FromStandardName(leaf)

	var resp *http.Response
	var info api.FolderUploadinfoResponse
	opts := rest.Opts{
		Method:     "POST",
		Path:       "/folder/uploadinfo",
		Parameters: o.fs.baseParams(),
		Options:    options,
		MultipartParams: url.Values{
			"id": {directoryID},
		},
	}
	err = o.fs.pacer.Call(func() (bool, error) {
		resp, err = o.fs.srv.CallJSON(ctx, &opts, nil, &info)
		if err != nil {
			return shouldRetry(ctx, resp, err)
		}
		// Just check the download URL resolves - sometimes
		// the URLs returned by premiumize.me don't resolve so
		// this needs a retry.
		var u *url.URL
		u, err = url.Parse(info.URL)
		if err != nil {
			return true, fmt.Errorf("failed to parse download URL: %w", err)
		}
		_, err = net.LookupIP(u.Hostname())
		if err != nil {
			return true, fmt.Errorf("failed to resolve download URL: %w", err)
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("upload get URL http: %w", err)
	}
	if err = info.AsErr(); err != nil {
		return fmt.Errorf("upload get URL: %w", err)
	}

	// if file exists then rename it out the way otherwise uploads can fail
	uploaded := false
	var oldID = o.id
	if o.hasMetaData {
		newLeaf := leaf + "." + random.String(8)
		fs.Debugf(o, "Moving old file out the way to %q", newLeaf)
		err = o.fs.renameLeaf(ctx, true, oldID, newLeaf)
		if err != nil {
			return fmt.Errorf("upload rename old file: %w", err)
		}
		defer func() {
			// on failed upload rename old file back
			if !uploaded {
				fs.Debugf(o, "Renaming old file back (from %q to %q) since upload failed", leaf, newLeaf)
				newErr := o.fs.renameLeaf(ctx, true, oldID, leaf)
				if newErr != nil && err == nil {
					err = fmt.Errorf("upload renaming old file back: %w", newErr)
				}
			}
		}()
	}

	opts = rest.Opts{
		Method:  "POST",
		RootURL: info.URL,
		Body:    in,
		MultipartParams: url.Values{
			"token": {info.Token},
		},
		MultipartContentName: "file", // ..name of the parameter which is the attached file
		MultipartFileName:    leaf,   // ..name of the file for the attached file
		ContentLength:        &size,
	}
	var result api.Response
	err = o.fs.pacer.CallNoRetry(func() (bool, error) {
		resp, err = o.fs.srv.CallJSON(ctx, &opts, nil, &result)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("upload file http: %w", err)
	}
	if err = result.AsErr(); err != nil {
		return fmt.Errorf("upload file: %w", err)
	}

	// on successful upload, remove old file if it exists
	uploaded = true
	if o.hasMetaData {
		fs.Debugf(o, "Removing old file")
		err := o.fs.remove(ctx, oldID)
		if err != nil {
			return fmt.Errorf("upload remove old file: %w", err)
		}
	}

	o.hasMetaData = false
	return o.readMetaData(ctx)
}

// Rename the leaf of a file or directory in a directory
func (f *Fs) renameLeaf(ctx context.Context, isFile bool, id string, newLeaf string) (err error) {
	opts := rest.Opts{
		Method: "POST",
		MultipartParams: url.Values{
			"id":   {id},
			"name": {newLeaf},
		},
		Parameters: f.baseParams(),
	}
	if isFile {
		opts.Path = "/item/rename"
	} else {
		opts.Path = "/folder/rename"
	}
	var resp *http.Response
	var result api.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.srv.CallJSON(ctx, &opts, nil, &result)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("rename http: %w", err)
	}
	if err = result.AsErr(); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Remove an object by ID
func (f *Fs) remove(ctx context.Context, id string) (err error) {
	opts := rest.Opts{
		Method: "POST",
		Path:   "/item/delete",
		MultipartParams: url.Values{
			"id": {id},
		},
		Parameters: f.baseParams(),
	}
	var resp *http.Response
	var result api.Response
	err = f.pacer.Call(func() (bool, error) {
		resp, err = f.srv.CallJSON(ctx, &opts, nil, &result)
		return shouldRetry(ctx, resp, err)
	})
	if err != nil {
		return fmt.Errorf("remove http: %w", err)
	}
	if err = result.AsErr(); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

// Remove an object
func (o *Object) Remove(ctx context.Context) error {
	err := o.readMetaData(ctx)
	if err != nil {
		return fmt.Errorf("Remove: Failed to read metadata: %w", err)
	}
	return o.fs.remove(ctx, o.id)
}

// MimeType of an Object if known, "" otherwise
func (o *Object) MimeType(ctx context.Context) string {
	return o.mimeType
}

// ID returns the ID of the Object if known, or "" if not
func (o *Object) ID() string {
	return o.id
}

// Check the interfaces are satisfied
var (
	_ fs.Fs              = (*Fs)(nil)
	_ fs.Purger          = (*Fs)(nil)
	_ fs.Mover           = (*Fs)(nil)
	_ fs.DirMover        = (*Fs)(nil)
	_ fs.DirCacheFlusher = (*Fs)(nil)
	_ fs.Abouter         = (*Fs)(nil)
	_ fs.PublicLinker    = (*Fs)(nil)
	_ fs.Object          = (*Object)(nil)
	_ fs.MimeTyper       = (*Object)(nil)
	_ fs.IDer            = (*Object)(nil)
)
