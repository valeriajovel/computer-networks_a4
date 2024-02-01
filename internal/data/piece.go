package data

import "sort"

type TorrentPiece struct {
	Status       bool
	Hash         string
	FileOffset   uint32
	Index        uint32
	Begin        uint32
	Length       uint32
	Block        []byte
	NumPeersHave int
	Rarity       float32
}

func SortTorrentPieces(pieces []TorrentPiece) []TorrentPiece {
	sort.Slice(pieces, func(i, j int) bool {
		return pieces[i].Rarity < pieces[j].Rarity
	})
	return pieces
}
