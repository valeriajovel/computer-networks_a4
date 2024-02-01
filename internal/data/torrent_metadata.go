package data

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"os"

	"github.com/marksamman/bencode"
)

type TorrentMetadata struct {
	Announce     string
	AnnounceList []string
	CreationDate *int64
	Comment      *string
	CreatedBy    *string
	Encoding     *string
	Info_hash    string
	NumPieces    int64
	Pieces       []TorrentPiece
	PieceLength  int64
	TotalLength  int64
	Private      int
	Files        []FileMetadata
}

/**
 * Takes a torrent file path and parses it as a TorrentMetadata struct
 *
 * @param torrentFile The path to the torrent file
 */
func (tm *TorrentMetadata) ParseTorrentFile(torrentFile string) error {
	file, err := os.Open(torrentFile)
	if err != nil {
		return fmt.Errorf("Error opening torrent file")
	}
	defer file.Close()

	infoDict, err := bencode.Decode(file)
	if err != nil {
		return fmt.Errorf("Error decoding torrent file into bencode dictionary")
	}

	// Set the fields of the TorrentMetadata structs from the infoDict
	// Parsing the info field
	if infoDict["info"] == nil {
		return fmt.Errorf("Torrent file is missing the \"info\" field")
	}
	err = tm.parseTorrentInfo(infoDict["info"].(map[string]interface{}))
	if err != nil {
		return err
	}
	infoByteArray := bencode.Encode(infoDict["info"])
	sha1bytes := sha1.Sum(infoByteArray)
	tm.Info_hash = string(sha1bytes[:])

	// Parsing the announce field
	if infoDict["announce"] == nil {
		return fmt.Errorf("Torrent file is missing the \"annouce\" field")
	}
	tm.Announce = infoDict["announce"].(string)

	// Parsing the announce list field if it exists
	if infoDict["announce-list"] != nil {
		dictAnnounceList := infoDict["announce-list"].([]interface{})
		tm.AnnounceList = make([]string, len(infoDict["announce-list"].([]interface{})))
		for index, value := range dictAnnounceList {
			url := value.([]interface{})[0].(string)
			tm.AnnounceList[index] = fmt.Sprintf(url)
		}
	} else {
		tm.AnnounceList = nil
	}

	// Parsing the creation date field
	if infoDict["creation date"] != nil {
		tm.CreationDate = new(int64)
		*tm.CreationDate = infoDict["creation date"].(int64)
	} else {
		tm.CreationDate = nil
	}

	// Parsing the comment field
	if infoDict["comment"] != nil {
		tm.Comment = new(string)
		*tm.Comment = infoDict["comment"].(string)
	} else {
		tm.Comment = nil
	}

	// Parsing the created by field
	if infoDict["created by"] != nil {
		tm.CreatedBy = new(string)
		*tm.CreatedBy = infoDict["created by"].(string)
	} else {
		tm.CreatedBy = nil
	}

	// Parsing the encoding field
	if infoDict["encoding"] != nil {
		tm.Encoding = new(string)
		*tm.Encoding = infoDict["encoding"].(string)
	} else {
		tm.Encoding = nil
	}

	return nil
}

/**
 * Parses the 'info' key value in the torrent bencode dictionary filling out the appropriate fields in the TorrentMetadata struct
 *
 * @param torrentInfo The info dictionary take from the 'info' section of the torrent file
 */
func (tm *TorrentMetadata) parseTorrentInfo(torrentInfo map[string]interface{}) error {
	var err error

	// Create a list of files for the 'file' field in the TorrentMetadata struct
	if torrentInfo["files"] == nil {
		tm.Files = make([]FileMetadata, 1)
		fileData, err := parseFile(torrentInfo)
		if err != nil {
			return err
		}

		tm.Files[0] = fileData

		// All the pieces will be for this single file
		for pieceIndex := 0; pieceIndex < int(tm.NumPieces); pieceIndex++ {
			tm.Files[0].PieceIndexes = append(tm.Files[0].PieceIndexes, []int{pieceIndex}...)
		}
	} else {
		files := torrentInfo["files"].([]interface{})
		tm.Files = make([]FileMetadata, len(files))
		currentPieceIndex := 0

		for index, file := range files {
			tm.Files[index], err = parseFile(file.(map[string]interface{}))
			if err != nil {
				return err
			}

			// Calculate the pieces associated with the file
			remainingLength := tm.Files[index].Length
			for currentPieceIndex < int(tm.NumPieces) && remainingLength > 0 {
				tm.Files[index].PieceIndexes = append(tm.Files[index].PieceIndexes, []int{currentPieceIndex}...)
				pieceLength := tm.Pieces[currentPieceIndex].Length
				remainingLength -= pieceLength

				// If we don't use all of the piece, the next file will also use the piece
				if remainingLength-pieceLength > 0 {
					currentPieceIndex += 1
				}
			}
		}
	}

	// Parse the piece length
	if torrentInfo["piece length"] == nil {
		return fmt.Errorf("Torrent file is missing the \"piece length\" field")
	}
	tm.PieceLength = torrentInfo["piece length"].(int64)

	// Calculate the number of pieces and total length
	// Help from https://stackoverflow.com/questions/2745074/fast-ceiling-of-an-integer-division-in-c-c
	if torrentInfo["files"] == nil {
		tm.NumPieces = (torrentInfo["length"].(int64) + tm.PieceLength - 1) / tm.PieceLength
		tm.TotalLength = torrentInfo["length"].(int64)
	} else {
		// Since we have multiple files, we need to get a sum of their lengths
		totalLength := int64(0)
		for _, file := range tm.Files {
			totalLength += int64(file.Length)
		}

		tm.NumPieces = (totalLength + tm.PieceLength - 1) / tm.PieceLength
		tm.TotalLength = totalLength
	}

	// Parse the piece hashes
	if torrentInfo["pieces"] == nil {
		return fmt.Errorf("Torrent file is missing the \"pieces\" field")
	}
	hashes := torrentInfo["pieces"].(string)

	for i := uint32(0); i < uint32(tm.NumPieces); i++ {
		hashString := string([]byte(hashes[i*20 : (i+1)*20]))
		var pieceLength int64
		if int64(i+1)*tm.PieceLength > tm.TotalLength {
			pieceLength = tm.TotalLength - (int64(i) * tm.PieceLength)
		} else {
			pieceLength = tm.PieceLength
		}

		piece := TorrentPiece{Status: false, Hash: hashString, Index: i, Begin: uint32(int64(i) * tm.PieceLength), Length: uint32(pieceLength), Block: nil, NumPeersHave: 0, Rarity: float32(0)}
		temp := []TorrentPiece{piece}
		tm.Pieces = append(tm.Pieces, temp...)
	}

	// Parse the private field if it exists
	if torrentInfo["private"] != nil {
		tm.Private = 1
	} else {
		tm.Private = 0
	}

	return nil
}

func CheckPiece(hash string, pieceToCheck []byte) bool {
	pieceHash := sha1.Sum(pieceToCheck)
	res := bytes.Equal(pieceHash[:], []byte(hash))
	return res
}
