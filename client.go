package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/dustin/go-humanize"
)

const clearScreen = "\033[H\033[2J"

var isHTTP = regexp.MustCompile(`^https?:\/\/`)

// ClientError formats errors coming from the client.
type ClientError struct {
	Type   string
	Origin error
}

func (clientError ClientError) Error() string {
	return fmt.Sprintf("Error %s: %s\n", clientError.Type, clientError.Origin)
}

// Client manages the torrent downloading.
type Client struct {
	Client   *torrent.Client
	Torrent  torrent.Torrent
	Progress int64
	Port     int
}

// NewClient creates a new torrent client based on a magnet or a torrent file.
// If the torrent file is on http, we try downloading it.
func NewClient(torrentPath string, port int, seed bool) (client Client, err error) {
	var t torrent.Torrent
	var c *torrent.Client

	client.Port = port

	// Create client.
	c, err = torrent.NewClient(&torrent.Config{
		DataDir:  os.TempDir(),
		NoUpload: !seed,
		Seed:     seed,
	})

	if err != nil {
		return client, ClientError{Type: "creating torrent client", Origin: err}
	}

	client.Client = c

	// Add torrent.

	// Add as magnet url.
	if strings.HasPrefix(torrentPath, "magnet:") {
		if t, err = c.AddMagnet(torrentPath); err != nil {
			return client, ClientError{Type: "adding torrent", Origin: err}
		}
	} else {
		// Otherwise add as a torrent file.

		// If it's online, we try downloading the file.
		if isHTTP.MatchString(torrentPath) {
			if torrentPath, err = downloadFile(torrentPath); err != nil {
				return client, ClientError{Type: "downloading torrent file", Origin: err}
			}
		}

		// Check if the file exists.
		if _, err = os.Stat(torrentPath); err != nil {
			return client, ClientError{Type: "file not found", Origin: err}
		}

		if t, err = c.AddTorrentFromFile(torrentPath); err != nil {
			return client, ClientError{Type: "adding torrent to the client", Origin: err}
		}
	}

	client.Torrent = t

	go func() {
		<-t.GotInfo()
		t.DownloadAll()

		// Prioritize first 5% of the file.
		for i := 0; i < len(t.Pieces)/100*5; i++ {
			t.Pieces[i].Priority = torrent.PiecePriorityReadahead
		}
	}()

	return
}

// Close cleans up the connections.
func (c *Client) Close() {
	c.Torrent.Drop()
	c.Client.Close()
}

// Render outputs the command line interface for the client.
func (c *Client) Render() {
	t := c.Torrent

	var currentProgress = t.BytesCompleted()
	speed := humanize.Bytes(uint64(currentProgress-c.Progress)) + "/s"
	c.Progress = currentProgress

	complete := humanize.Bytes(uint64(currentProgress))
	size := humanize.Bytes(uint64(t.Length()))

	print(clearScreen)
	fmt.Println(t.Name())
	fmt.Println("=============================================================")
	if c.ReadyForPlayback() {
		fmt.Printf("Stream: \thttp://localhost:%d\n", c.Port)
	}

	if currentProgress > 0 {
		fmt.Printf("Progress: \t%s / %s  %.2f%%\n", complete, size, c.percentage())
	}
	if currentProgress < t.Length() {
		fmt.Printf("Download speed: %s\n", speed)
	}
	fmt.Printf("Connections: \t%d\n", len(t.Conns))
	//fmt.Printf("%s\n", c.RenderPieces())
}

func (c Client) getLargestFile() *torrent.File {
	var target torrent.File
	var maxSize int64

	for _, file := range c.Torrent.Files() {
		if maxSize < file.Length() {
			maxSize = file.Length()
			target = file
		}
	}

	return &target
}

/*
func (c Client) RenderPieces() (output string) {
	for i := range c.Torrent.Pieces {
		piece := c.Torrent.Pieces[i]

		if piece.PublicPieceState.Priority == torrent.PiecePriorityReadahead {
			output += "!"
		}

		if piece.PublicPieceState.Partial {
			output += "P"
		} else if piece.PublicPieceState.Checking {
			output += "c"
		} else if piece.PublicPieceState.Complete {
			output += "d"
		} else {
			output += "_"
		}
	}

	return
}
*/

// ReadyForPlayback checks if the torrent is ready for playback or not.
// we wait until 5% of the torrent to start playing.
func (c Client) ReadyForPlayback() bool {
	return c.percentage() > 5
}

// GetFile is an http handler to serve the biggest file managed by the client.
func (c Client) GetFile(w http.ResponseWriter, r *http.Request) {
	target := c.getLargestFile()
	entry, err := NewFileReader(c, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	defer func() {
		if err := entry.Close(); err != nil {
			log.Printf("Error closing file reader: %s\n", err)
		}
	}()

	w.Header().Set("Content-Disposition", "attachment; filename=\""+c.Torrent.Name()+"\"")
	http.ServeContent(w, r, target.DisplayPath(), time.Now(), entry)
}

func (c Client) percentage() float64 {
	return float64(c.Torrent.BytesCompleted()) / float64(c.Torrent.Length()) * 100
}

func downloadFile(URL string) (fileName string, err error) {
	var file *os.File
	if file, err = ioutil.TempFile(os.TempDir(), "torrent-imageviewer"); err != nil {
		return
	}

	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("Error closing torrent file: %s", err)
		}
	}()

	response, err := http.Get(URL)
	if err != nil {
		return
	}

	defer func() {
		if err := response.Body.Close(); err != nil {
			log.Printf("Error closing torrent file: %s", err)
		}
	}()

	_, err = io.Copy(file, response.Body)

	return file.Name(), err
}
