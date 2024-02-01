package main

import (
	"bittorrent/internal/core"
	"log"
	"os"
)

/**
 * Usage: ./client [torrent file]
 */
func main() {
	// Make sure there is only one argument
	if len(os.Args) > 3 || len(os.Args) < 3 {
		log.Fatal("Usage: client [torrent file] [output path]")
	}

	// Read the torrent file into a map
	var torrentFile string = os.Args[1]
	var outputPath string = os.Args[2]

	// Parse the torrent file
	_, err := core.NewClient(torrentFile, outputPath)
	if err != nil {
		log.Fatal(err)
	}

}
