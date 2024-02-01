package data

import (
	"encoding/binary"
	//"log"
	"math/rand"
)

/*

todos:
- put time limit on download attempt

- ****check hash of piece at end of piece download?

- how to stop downloads in progress? where? (remember to add work to queue if download failed but not if someone else did it)
- dealing with situation where no peer has a specific piece (should be knowable via bitfields - OR all bitfields, if a zero remains)
- error handling
- don't add all pieces to todo, check if they are downloaded already (in case of non first set of peers)
*/

func ReceiveRoutine(peer *Peer) {
	//ticker := time.NewTicker(5 * time.Second)
	for peer.IsNeeded {
		// TODO: Can we return the message as a r2sChStruct if it needs to go to send routine??
		err := peer.parseMessage()
		if err != nil {
			// BAD!!!
			//log.Println(err)
			continue
		} else {
			if peer.AmChoking {
				peer.SendChoke()
			} else {
				peer.SendUnchoke()
			}

			/*
				select {
				case <-ticker.C:
					log.Println("ticker")
								default:
					log.Println
					continue
				}
			*/
		}
	}
}

func SendRoutine(peer *Peer, todoCh, prioCh chan TorrentPiece, doneCh chan DoneChStruct) {

	// create empty pieceStatus struct, -1 in index means we are not busy
	status := pieceStatus{}

	// track pieces we have already "cloned" (i.e. added to prioCh multiple times)
	var dupedPieces []uint32

	for peer.IsNeeded {
		//log.Printf("Pending: %d", status.pending)
		var p *TorrentPiece
		p = nil
		select {
		case have_index := <-peer.HaveCh:
			// Send a "have" message
			peer.SendHave(uint32(have_index))

		case msg := <-peer.R2sCh:
			// Process data received from the receiveRoutine
			if msg.ID == 7 { // piece from peer
				//var msgIndex uint32
				var msgBegin uint32
				//var msgBlock []byte

				//binary.BigEndian.PutUint32(msg.Data[0:4], msgIndex)
				//msgIndex = binary.BigEndian.Uint32(msg.Data[0:4])
				//binary.BigEndian.PutUint32(msg.Data[4:8], msgBegin)
				msgBegin = binary.BigEndian.Uint32(msg.Data[4:8])
				//copy(msgBlock, msg.Data[8:])
				//log.Printf("Received piece %d, begin %d", msgIndex, msgBegin)

				copy(status.buf[msgBegin:], msg.Data[8:])
				peer.Bmu.Lock()
				peer.Bytes += 1
				peer.Bmu.Unlock()
				status.pending--
			}

		default:
			if !status.busy && peer.PeerChoking == false { // ignore todoCh/prioCh if we are working with a piece already
				select {
				case piece := <-prioCh:
					p = &piece
					//log.Printf("Trying to download piece %d from %s", p.Index, peer.Connection.RemoteAddr().String())
					// Check if peer has the piece, try to download if they have it
					// If they don't have it, check if it is downloaded
					// If not, add it back to prioCh 'n' times where n is min(# attempts, 3)

					if peer.HasPiece(p.Index) && p.Status == false { // peer has, we don't -> proceed with dl

						status.busy = true
						status.index = p.Index
						status.buf = make([]byte, p.Length)
						status.left = p.Length
						status.length = p.Length
						status.pending = 0
						status.hash = p.Hash

					} else if !peer.HasPiece(p.Index) && p.Status == false { // both we and peer don't have -> add to prio queue 2x (someone should have it)

						prioCh <- *p
						prioCh <- *p
						dupedPieces = append(dupedPieces, p.Index)

					} else { // we already have piece, don't add it back onto prioCh
						continue
					}

				case piece := <-todoCh:
					p = &piece
					//log.Printf("Trying to download piece %d from %s", p.Index, peer.Connection.RemoteAddr().String())
					// Check if peer has the piece, try to download if they have it
					// If they don't have it, add it to prioCh as a paStruct (piece, 1)
					if peer.HasPiece(p.Index) && p.Status == false { // peer has, we don't -> proceed with dl

						status.busy = true
						status.index = p.Index
						status.buf = make([]byte, p.Length)
						status.left = p.Length
						status.length = p.Length
						status.pending = 0
						status.hash = p.Hash

					} else if !peer.HasPiece(p.Index) && p.Status == false { // both we and peer don't have -> add to prio queue 2x (someone should have it)

						prioCh <- *p

					} else { // this should be unreachable since todoCh contains no dupes
						continue
					}
				default:
					continue
				}
			}
		}

		// if status is not -1, we are either currently downloading or just finished a download
		if status.busy {
			//log.Println("status busy")
			// up to 5 outgoing requests should be sent at a time (or fewer if <5 pieces are remaining)
			for status.pending < 5 && status.left > 0 {
				var block_size uint32 = 16384
				if status.left < 16384 { // the size of the block we are requesting should be the max size, or it is the final block and can be less than max
					block_size = status.left
				}

				// make request to peer
				// send request, start index depends on how much we have left to receive and how many messages we are waiting on
				//log.Println("sending request")
				peer.SendRequest(status.index, status.length-status.left, block_size)
				status.left -= block_size
				status.pending++

			}
		}

		// If status.busy == true, we are trying to download a piece.
		// When pending is 0 and left is 0, all blocks have been received and we can send the full piece to doneCh
		if status.busy && status.pending == 0 && status.left == 0 {
			//log.Println("finished piece")
			if CheckPiece(status.hash, status.buf) {
				fin := DoneChStruct{}
				fin.Index = status.index
				fin.Buf = make([]byte, status.length)
				copy(fin.Buf[:], status.buf[:])
				doneCh <- fin
				status.busy = false
			} else {
				status.busy = false

			}
		}

		if peer.PeerChoking == true {
			if p != nil {
				prioCh <- *p
			}
			status.busy = false
		}
	}
	//log.Println("exited")
}

// contains method for ints (used to see if a piece is in dupedPieces slice)
func contains(slice []int, index int) bool {
	for _, item := range slice {
		if item == index {
			return true
		}
	}
	return false
}

/*
messages that go from receive goroutine to send goroutine, will have same types of peer messages
essentially, when send goroutine receives a r2sChStruct, it is time to send that message out

when do we need to actually send each message type?
- choke/0 : when the outer loop decides to choke a currently unchoked peer
- unchoke/1 : when the outer loop decides to unchoke a currently choked peer (optimistic or start of torrent)
- interested/2 : when the peer has a piece we don't have (they are one of our downloaders)
- not interested/3 : when peer has nothing we don't have
- have/4 : when we successfully download a piece, we can send a have (from haveCh)
- bitfield/5 : we should already have sent it in/after handshake
- request/6 : when we are downloading a piece
- piece/7 : in response to a request from an unchoked peer
- cancel/8 : only in endgame mode??

when will recv routine send these to the send routine >> and what does the send routine need to do?
- choke/0 : same as above >> send choke and reset the status struct
- unchoke/1 : same as above >> send unchoke
- interested/2 : when the interested variable in the peer has changed to true (prev false) >> send interested
- not interested/3 : when the interested variable has changed to false (prev true) >> send not interested, reset status struct
- have/4 : recv does not send these, send will read from haveCh >> send have
- bitfield/5 : never >> never
- request/6 : when peer sends us a request >> send the piece as specified
- piece/7 : when peer sends us a block >> fill buffer, send more requests
- cancel/8 : TODO
*/

type DoneChStruct struct {
	Index uint32
	Buf   []byte
}

/*
	Struct that holds the status of a piece that is in the process of being downloaded

- index: the index of the piece in the file
- buf: the buffer that holds the piece
- left: how many bytes are left to be received
- pending: how many block requests are outgoing
*/
type pieceStatus struct {
	busy    bool
	index   uint32
	buf     []byte
	left    uint32
	pending uint32
	length  uint32
	hash    string
}

// chooses up to 15 interesting peers to download from if there are not already 15
func ChooseDownloaders(peers []Peer, currDownloaders []*Peer) []*Peer {

	if len(currDownloaders) == 0 { // No current downloaders, select peers that have unchoked us and we are interested in them
		j := 0
		for j < len(peers) && len(currDownloaders) < 15 {

			peer := &peers[j]
			peer.Bmu.Lock()
			if !peer.PeerChoking && peer.AmInterested { // if they unchoked us and we are interested, they can be one of our downloaders
				currDownloaders = append(currDownloaders, peer)
			}
			j++
			peer.Bmu.Unlock()
		}
	} else { // we have some downloaders, make sure we have 15
		needMore := min(15, len(peers)) - len(currDownloaders)

		if needMore > 0 { // if we need more peers, make a map of peer/bool, to keep track of which peers are current downloaders
			currDownloadersMap := make(map[*Peer]bool)
			for i := 0; i < len(currDownloaders); i++ {
				currDownloadersMap[currDownloaders[i]] = true
			}

			for needMore > 0 {
				for {
					randomPeerIndex := rand.Intn(len(peers))
					if !currDownloadersMap[&peers[randomPeerIndex]] {
						currDownloaders = append(currDownloaders, &peers[randomPeerIndex])
						currDownloadersMap[&peers[randomPeerIndex]] = true
						break
					}

				}
				needMore--
			}
		}
	}
	return currDownloaders
}

// chooses up to three interested peers that have the best download speeds
func ChooseStablePeers(peers []Peer) []*Peer {

	bdArray := make([]int, len(peers))
	h := 0
	for h < len(peers) {
		peers[h].Bmu.Lock()
		bdArray[h] = int(peers[h].Bytes)
		peers[h].Bytes = 0
		peers[h].Bmu.Unlock()
		h++
	}

	uc1 := -1
	uc2 := -1
	uc3 := -1

	i := 0
	for i < len(peers) {
		currPeerBytes := bdArray[i]
		if peers[i].AmInterested { // if peer is interested in us,
			if uc1 == -1 || currPeerBytes > bdArray[uc1] {
				uc3 = uc2
				uc2 = uc1
				uc1 = i
			} else if uc2 == -1 || currPeerBytes > bdArray[uc2] {
				uc3 = uc2
				uc2 = i
			} else if uc3 == -1 || currPeerBytes > bdArray[uc3] {
				uc3 = i
			}

		}
		i++
	}

	chosenPeers := make([]*Peer, 0)
	if uc1 != -1 {
		chosenPeers = append(chosenPeers, []*Peer{&peers[uc1]}...)
		if uc2 != -1 {
			chosenPeers = append(chosenPeers, []*Peer{&peers[uc2]}...)
			if uc3 != -1 {
				chosenPeers = append(chosenPeers, []*Peer{&peers[uc3]}...)
			}
		}
	}

	return chosenPeers
}

// returns a random interested, choked peer if 1+ are available, else returns a nil pointer
func ChooseOUPeer(peers []Peer) *Peer {
	chokedButInterestedMap := make(map[*Peer]bool)
	possible := false
	var ouPeer *Peer

	for i := 0; i < len(peers); i++ {
		if peers[i].AmChoking && peers[i].PeerInterested {
			chokedButInterestedMap[&peers[i]] = true
			possible = true
		}
	}

	for possible {
		randomPeerIndex := rand.Intn(len(peers))
		if chokedButInterestedMap[&peers[randomPeerIndex]] {
			ouPeer = &peers[randomPeerIndex]
			break
		}

	}

	return ouPeer
}
