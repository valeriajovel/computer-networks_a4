package core

import (
	"bittorrent/internal/data"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/marksamman/bencode"
)

type Client struct {
	Torrent           data.TorrentMetadata
	Tracker           data.TrackerInformation
	Peer_id           string
	Listen_connection net.Listener
}

const charset = "0123456789"
const HTTPSDEBUG = false

/*
TODOS:
- send out initial interested
- at some point, we need to set fields to false (if we choose a new OU peer, or if we find a better uploader), track previous round's special peers?
*/

const (
	Started   int = 0
	Completed int = 1
	Stopped   int = 2
	MorePeers int = 3
)

/**
 * Returns a random peer-id in Azureus-style
 *
 * @note Help from https://www.tutorialspoint.com/how-to-generate-random-string-characters-in-golang
 */
func generatePeerId() string {
	randNumString := make([]byte, 12)
	var seededRand *rand.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range randNumString {
		randNumString[i] = charset[seededRand.Intn(len(charset))]
	}
	return fmt.Sprintf("-BT0001-%s", randNumString)
}

func NewClient(torrentFile, outputPath string) (*Client, error) {
	client := &Client{}

	// Parse the torrent file
	err := client.Torrent.ParseTorrentFile(torrentFile)
	if err != nil {
		return nil, err
	}

	// Create a peer id for the client
	client.Peer_id = generatePeerId()

	selfBitfield := big.NewInt(0)

	var lc net.ListenConfig
	ctx, cancel := context.WithCancel(context.Background())
	// Try to TCP listener bound to a port between 6881-6889
	for i := 6881; i <= 6889; i++ {
		listener, err := lc.Listen(ctx, "tcp", fmt.Sprintf("0.0.0.0:%d", i))
		if err != nil {
			continue
		}
		client.Listen_connection = listener
		break
	}
	defer cancel()
	// If we failed to create a TCP listener, fail
	if client.Listen_connection == nil {
		return nil, fmt.Errorf("Failed to create a TCP listener between ports 6881-6889")
	}

	client.Tracker.PeersMutex.Lock()
	client.Tracker.Peers = make([]data.Peer, 0)
	client.Tracker.PeersMutex.Unlock()

	err = client.getTrackerResponse(Started)
	if err != nil {
		return nil, err
	}

	// TODO? If we have 0 connected peers, ask for more peers
	todoCh := make(chan data.TorrentPiece, client.Torrent.NumPieces)
	prioCh := make(chan data.TorrentPiece, client.Torrent.NumPieces)
	doneCh := make(chan data.DoneChStruct, client.Torrent.NumPieces)
	writtenCh := make(chan int64, client.Torrent.NumPieces)

	// Calculate initial piece rarity
	// Iterate over connected peers that have sent us a bitfield after the initial handshake
	// For each piece, calculate the rarity = # of peers that have that piece / total # of peers
	// ? Maybe give a channel for peers to send initial haves in ConnectToPeer (some clients might not have received a bitfield at this point
	// ? if their peer sent it over)
	/*
		client.Tracker.PeersMutex.RLock()
		for i := 0; i < len(client.Tracker.Peers); i++ {
			for i := uint32(0); i < uint32(client.Torrent.NumPieces); i++ {
				if client.Tracker.Peers[i].Haves.Bit(int(i)) == 1 {
					client.Torrent.Pieces[i].NumPeersHave += (1 / len(client.Tracker.Peers))
				}
			}
		}
		client.Tracker.PeersMutex.RUnlock()
	*/

	// TODO! dont add all
	sortedPieces := data.SortTorrentPieces(client.Torrent.Pieces)
	for _, piece := range sortedPieces {
		todoCh <- piece
	}
	close(todoCh)

	// Start routines within peers and pass channels
	client.Tracker.PeersMutex.Lock()
	// for _, peer := range client.Tracker.Peers {
	j := 0
	for j < len(client.Tracker.Peers) {
		peer := &client.Tracker.Peers[j]
		peer.R2sCh = make(chan data.Message, 5)
		peer.HaveCh = make(chan uint32, 5)
		go data.ReceiveRoutine(peer)
		go data.SendRoutine(peer, todoCh, prioCh, doneCh)
		j++
	}
	client.Tracker.PeersMutex.Unlock()

	// Start file writer routine
	go client.fileWriterRoutine(outputPath, writtenCh, doneCh)

	// Start ticker
	ticker := time.NewTicker(5 * time.Second)

	// Run a goroutine to handle the ticker
	go func() {
		min := true
		currDownloaders := make([]*data.Peer, 0)
		stablePeers := make([]*data.Peer, 0)
		var ouPeer *data.Peer
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				client.Tracker.PeersMutex.Lock()
				if min { // decides who to unchoke and who to signal interested
					stablePeers = data.ChooseStablePeers(client.Tracker.Peers)
					for i := 0; i < len(stablePeers); i++ {
						if stablePeers[i] != nil {
							stablePeers[i].AmChoking = false
						}
					}

					ouPeer = data.ChooseOUPeer(client.Tracker.Peers)
					if ouPeer != nil {
						ouPeer.AmChoking = false
					}

					currDownloaders = data.ChooseDownloaders(client.Tracker.Peers, currDownloaders)
					//log.Printf("Current amount of downloaders: %d\n", len(currDownloaders))
					for i := 0; i < len(client.Tracker.Peers); i++ {
						client.Tracker.Peers[i].IsDownloader = false
					}
					for i := 0; i < len(currDownloaders); i++ {
						if currDownloaders[i] != nil {
							//log.Println(currDownloaders[i].Connection.RemoteAddr().String())
							currDownloaders[i].IsDownloader = true
							currDownloaders[i].AmInterested = true
						}
					}

					min = false
				} else { // decides who to optimistically unchoke
					ouPeer = data.ChooseOUPeer(client.Tracker.Peers)
					if ouPeer != nil {
						ouPeer.AmChoking = false
					}
					min = true
				}
				client.Tracker.PeersMutex.Unlock()
			default:
				time.Sleep(2 * time.Second)
			}
		}
	}()

	// Run a goroutine to listen for peers trying to connect to us
	go func() {
		// So we can gracefully shutdown at a later point
		go func() {
			<-ctx.Done()
			client.Listen_connection.Close()
		}()

		for {
			peerConn, err := client.Listen_connection.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					log.Printf("Unexpected error while accepting peer [ERROR]: %s", err)
					return
				}
			}

			go func() {
				// Establish the Ip address and port
				tempPeer := data.Peer{}
				splitAddress := strings.Split(peerConn.RemoteAddr().String(), ":")
				tempPeer.Ip = net.ParseIP(splitAddress[0])
				tempPeerPort, err := strconv.ParseUint(splitAddress[1], 10, 16)
				if err != nil {
					log.Printf("Failed to accept peer at: %s [ERROR]: %s", peerConn.RemoteAddr().String(), err)
					return
				}
				tempPeer.Port = uint16(tempPeerPort)

				// Create a hashset so we can check to see if the connection already exists
				alreadyConnectedPeers := make(map[string]bool)
				for i := 0; i < len(client.Tracker.Peers); i++ {
					peerIpAddr := client.Tracker.Peers[i].Ip.String()
					peerPort := client.Tracker.Peers[i].Port
					peerAddr := fmt.Sprintf("%s:%d", peerIpAddr, peerPort)
					alreadyConnectedPeers[peerAddr] = true
				}

				// Check to see if the connection already exists
				if _, exists := alreadyConnectedPeers[peerConn.RemoteAddr().String()]; !exists {
					// Really hacky to get around copying the peer bytes mutex
					tempPeerList := append(client.Tracker.Peers, []data.Peer{{Ip: tempPeer.Ip, Port: tempPeer.Port}}...)
					err := tempPeerList[len(tempPeerList)-1].ConnectToPeer(&client.Torrent)
					if err != nil {
						log.Printf("Failed to accept peer at: %s [ERROR]: %s", peerConn.RemoteAddr().String(), err)
					}

					client.Tracker.PeersMutex.Lock()
					client.Tracker.Peers = tempPeerList
					client.Tracker.PeersMutex.Unlock()

					err = client.Tracker.Peers[len(client.Tracker.Peers)-1].SendBitfield(*selfBitfield)
					if err != nil {
						log.Printf("Failed to send bitfield to peer at: %s [ERROR]: %s", peerConn.RemoteAddr().String(), err)
					}
				}
			}()
		}
	}()

	// TODO! Main loop
	// 30 sec timer for OU turnover -->
	// if min set, min false -> select peers to download from --> down_peers -> select stable peers to upload to (3) --> best_peers
	// else set min true
	// ? change min check above to 1 minute timer?
	// select (interested) peer for OU --> ou_peer
	// update peer structs as needed (download_from, unchoke)
	interestTicker := time.NewTicker(5 * time.Second)

	for result := new(big.Int).Not(selfBitfield).Cmp(new(big.Int).SetUint64(0)); result != 0; result = new(big.Int).Not(selfBitfield).Cmp(new(big.Int).SetUint64(0)) {
		// check writtenCh for new pieces have been received, if we get one, send haves
		select {
		case <-ctx.Done():
			break
		case writtenPieceIndex := <-writtenCh:
			for i := 0; i < len(client.Tracker.Peers); i++ {
				peer := &client.Tracker.Peers[i]
				peer.HaveCh <- uint32(writtenPieceIndex)
			}
		case <-interestTicker.C:
			for i := 0; i < len(client.Tracker.Peers); i++ {
				peer := &client.Tracker.Peers[i]
				unfinishedPieces := new(big.Int).Not(selfBitfield)
				sharedPiecesWithPeer := new(big.Int).And(unfinishedPieces, &peer.Haves)

				cmpResult := sharedPiecesWithPeer.Cmp(new(big.Int).SetInt64(0))
				switch cmpResult {
				case -1:
				case 0:
					if peer.AmInterested == true {
						peer.SendNotInterested()
					}
				case 1:
					if peer.AmInterested != true {
						peer.SendInterested()
					}
				}
			}
		default:
		}
	}

	// Stop listening for new connections
	cancel()

	// Close all connections
	for i := 0; i < len(client.Tracker.Peers); i++ {
		client.Tracker.Peers[i].IsNeeded = false
	}

	return client, nil
}

func (client *Client) getTrackerResponse(trackerRequestType int) error {
	// Try to fetch from the main announce address
	var HTTPEvent string
	var UDPEvent int
	if trackerRequestType == Started {
		HTTPEvent = "started"
		UDPEvent = 2
	} else if trackerRequestType == Stopped {
		HTTPEvent = "stopped"
		UDPEvent = 3
	} else if trackerRequestType == Completed {
		HTTPEvent = "completed"
		UDPEvent = 1
	} else if trackerRequestType == MorePeers {
		HTTPEvent = ""
		UDPEvent = 0
	} else {
		return fmt.Errorf("Unknown tracker request type")
	}

	var err error = nil
	if client.Torrent.Announce[:4] == "http" {
		log.Printf("Trying %s\n", client.Torrent.Announce)
		err = client.sendHTTPTrackerRequest(client.Torrent.Announce, 0, HTTPEvent)
		if err != nil {
			log.Println(err)
			err = client.sendHTTPTrackerRequest(client.Torrent.Announce, 1, HTTPEvent)
			if err != nil {
				log.Println(err)
			}
		}
	} else {
		err = client.sendUDPTrackerRequest(client.Torrent.Announce[6:], UDPEvent)
		if err != nil {
			log.Println(err)
		}
	}

	// If we failed to fetch from the main announce address, try the addresses in the announce list
	if err != nil {
		for _, announceAddress := range client.Torrent.AnnounceList {
			log.Printf("Trying %s\n", announceAddress)
			if announceAddress[:4] == "http" {
				err = client.sendHTTPTrackerRequest(announceAddress, 0, HTTPEvent)
				if err != nil {
					err = client.sendHTTPTrackerRequest(announceAddress, 1, HTTPEvent)
				}
			} else {
				err = client.sendUDPTrackerRequest(announceAddress[6:], UDPEvent)
			}

			// If fetching was successful, return from the function
			if err == nil {
				return nil
			}
		}
	} else {
		return nil
	}

	// Total failure
	return fmt.Errorf("Failed to fetch tracking information from all of the announce addresses")
}

func (client *Client) sendHTTPTrackerRequest(announceUrl string, compact int, event string) error {
	trackerRequestUrl := announceUrl
	// Establish the tracker request parameters
	// Set the 'info_hash'
	info_hash := url.QueryEscape(client.Torrent.Info_hash)
	trackerRequestUrl += fmt.Sprintf("?info_hash=%s", info_hash)
	// Set the 'peer_id'
	peer_id := url.QueryEscape(client.Peer_id)
	trackerRequestUrl += fmt.Sprintf("&peer_id=%s", peer_id)
	// Set the 'port'
	splitAddress := strings.Split(client.Listen_connection.Addr().String(), ":")
	port := splitAddress[len(splitAddress)-1]
	trackerRequestUrl += fmt.Sprintf("&port=%s", port)
	// Set the 'uploaded'
	uploaded := 0
	trackerRequestUrl += fmt.Sprintf("&uploaded=%d", uploaded)
	// Set the 'downloaded'
	downloaded := 0
	trackerRequestUrl += fmt.Sprintf("&downloaded=%d", downloaded)
	// Set the 'left'
	left := client.Torrent.TotalLength
	trackerRequestUrl += fmt.Sprintf("&left=%d", left)
	// Set the 'compact
	trackerRequestUrl += fmt.Sprintf("&compact=%d", compact)
	// Set the 'no_peer_id
	no_peer_id := 0
	trackerRequestUrl += fmt.Sprintf("&no_peer_id=%d", no_peer_id)
	// Set the 'event'
	if event != "" {
		trackerRequestUrl += fmt.Sprintf("&event=%s", event)
	}

	// Make the request
	var statusCode int
	var responseBody string
	var responseBodyReader io.Reader
	var err error
	if HTTPSDEBUG == false {
		statusCode, responseBody, err = sendHTTPRequest(trackerRequestUrl)
	} else {
		var response *http.Response
		response, err = http.Get(trackerRequestUrl)
		statusCode = response.StatusCode
		responseBodyReader = response.Body
		defer response.Body.Close()
	}
	if err != nil {
		return err
	}

	if statusCode != http.StatusOK {
		return fmt.Errorf("Non-OK HTTP Status while making a GET request to the tracker: %d", statusCode)
	}

	// Decode the response into a bencoded dictionary
	var trackerResponse map[string]interface{}
	if HTTPSDEBUG == false {
		responseBodyReader = strings.NewReader(responseBody)
		trackerResponse, err = bencode.Decode(strings.NewReader(responseBody))
	} else {
		trackerResponse, err = bencode.Decode(responseBodyReader)
	}
	if err != nil {
		return err
	}

	// Check to see if the tracker response came with a failure message
	if trackerResponse["failure reason"] != nil {
		return fmt.Errorf(trackerResponse["failure reason"].(string))
	}

	// If the tracker response has a warning message, log it
	if trackerResponse["warning message"] != nil {
		log.Printf("Tracker Warning: %s\n", trackerResponse["warning message"].(string))
	}

	// Update the tracker information associated with the client
	err = client.Tracker.UpdateTrackerInformation(&client.Torrent, trackerResponse)
	if err != nil {
		return err
	}

	return nil
}

/**
 *	Does the tracker interaction in UDP
 *  Adapted from https://xbtt.sourceforge.net/udp_tracker_protocol.html
 */
func (client *Client) sendUDPTrackerRequest(address string, event int) error {
	conn, err := net.Dial("udp", address)
	if err != nil {
		return err
	}
	defer conn.Close()

	transactionId := rand.Uint32()

	// Create and send the initial connect packet
	connectOutput, err := udpConnect(conn, transactionId)
	if err != nil {
		return err
	}

	action := int32(binary.BigEndian.Uint32(connectOutput))
	connectTransactionId := binary.BigEndian.Uint32(connectOutput[4:])
	connectionId := binary.BigEndian.Uint64(connectOutput[8:])

	if action != 0 {
		return fmt.Errorf("Unexpected action during the connect phase of the UDP tracker protocol")
	}

	if connectTransactionId != transactionId {
		return fmt.Errorf("Unexpected transaction ID during the connect phase of the UDP tracker protocol")
	}

	announceOutput, err := udpAnnouce(client, conn, transactionId, connectionId, event)
	if err != nil {
		return err
	}

	action = int32(binary.BigEndian.Uint32(announceOutput))
	announceTransactionId := binary.BigEndian.Uint32(announceOutput[4:])
	interval := int64(binary.BigEndian.Uint32(announceOutput[8:]))
	incomplete := int64(binary.BigEndian.Uint32(announceOutput[12:]))
	complete := int64(binary.BigEndian.Uint32(announceOutput[16:]))
	peers := string(announceOutput[20:])

	if action != 1 {
		return fmt.Errorf("Unexpected action during the announce phase of the UDP tracker protocol")
	}

	if announceTransactionId != transactionId {
		return fmt.Errorf("Unexpected transaction ID during the announce phase of the UDP tracker protocol")
	}

	trackerResponse := make(map[string]interface{})
	trackerResponse["min interval"] = interval
	trackerResponse["complete"] = complete
	trackerResponse["incomplete"] = incomplete
	trackerResponse["peers"] = peers

	err = client.Tracker.UpdateTrackerInformation(&client.Torrent, trackerResponse)
	if err != nil {
		return err
	}

	return nil
}

func udpConnect(udp_conn net.Conn, transactionId uint32) ([]byte, error) {
	buf := make([]byte, 16)
	binary.BigEndian.PutUint64(buf, 4497486125440)
	binary.BigEndian.PutUint32(buf[8:], 0)
	binary.BigEndian.PutUint32(buf[12:], transactionId)
	udp_conn.Write(buf)

	n := 0
	response := make([]byte, 16)
	responseReceived := false
	for responseReceived == false {
		// Help from https://stackoverflow.com/questions/26081073/how-do-i-read-a-udp-connection-until-a-timeout-is-reached
		temp := 60 * int(math.Pow(2, float64(n)))
		err := udp_conn.SetReadDeadline(time.Now().Add(time.Duration(temp) * time.Second))
		if err != nil {
			return nil, err
		}

		numBytesReceived, err := udp_conn.Read(response)
		if err != nil {
			if e, ok := err.(net.Error); !ok || !e.Timeout() {
				return nil, err
			} else if e.Timeout() {
				n += 1
				continue
			}
		}

		if numBytesReceived != 16 {
			return nil, fmt.Errorf("Connect response from the UDp tracker was not 16 bytes")
		}

		responseReceived = true
	}

	return response, nil
}

func udpAnnouce(client *Client, udp_conn net.Conn, transactionId uint32, connectionId uint64, event int) (data []byte, err error) {
	// Build the annouce input packet
	request := make([]byte, 98)
	// 0	64-bit integer	connection_id
	binary.BigEndian.PutUint64(request, connectionId)
	// 8	32-bit integer	action	1
	binary.BigEndian.PutUint32(request[8:], 1)
	// 12	32-bit integer	transaction_id
	binary.BigEndian.PutUint32(request[12:], transactionId)
	// 16	20-byte string	info_hash
	copy(request[16:], []byte(client.Torrent.Info_hash))
	// 36	20-byte string	peer_id
	copy(request[36:], []byte(client.Peer_id))
	// 56	64-bit integer	downloaded
	binary.BigEndian.PutUint64(request[56:], 0)
	// 64	64-bit integer	left
	binary.BigEndian.PutUint64(request[64:], uint64(client.Torrent.TotalLength))
	// 72	64-bit integer	uploaded
	binary.BigEndian.PutUint64(request[72:], 0)
	// 80	32-bit integer	event (2 for started)
	binary.BigEndian.PutUint32(request[80:], uint32(event))
	// 84	32-bit integer	IP address	0
	binary.BigEndian.PutUint32(request[84:], 0)
	// 88	32-bit integer	key
	binary.BigEndian.PutUint32(request[88:], rand.Uint32())
	// 92	32-bit integer	num_want	-1
	binary.BigEndian.PutUint32(request[92:], ^uint32(0))
	// 96	16-bit integer	port
	splitAddress := strings.Split(client.Listen_connection.Addr().String(), ":")
	portString := splitAddress[len(splitAddress)-1]
	port, err := strconv.ParseUint(portString, 16, 16)
	if err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint16(request[96:], uint16(port))
	udp_conn.Write(request)

	n := 0
	response := make([]byte, 65535)
	responseReceived := false
	numBytesReceived := 0
	for responseReceived == false {
		temp := 60 * int(math.Pow(2, float64(n)))
		err := udp_conn.SetReadDeadline(time.Now().Add(time.Duration(temp) * time.Second))
		if err != nil {
			return nil, err
		}

		numBytesReceived, err = udp_conn.Read(response)
		if err != nil {
			if e, ok := err.(net.Error); !ok || !e.Timeout() {
				return nil, err
			} else if e.Timeout() {
				n += 1
				continue
			}
		}

		responseReceived = true
	}

	return []byte(string(response[0:numBytesReceived])), nil
}

/*
FILE WRTIER PSUEDOCODE
for loop
select

	piece comes in doneCh -> check if it's the next piece in the current file
		if it is, write to file
			if not last piece in file
					discard piece
			else
				keep piece incase next file needs it
		else
			store piece

	no piece (default) -> check if next piece to write for current file is already downloaded
		if it is, write to it
			if not last piece in file
				discard piece
			else
				keep piece incase next file needs it
*/
/*
func (client *Client) fileWriterRoutine(outputPath string, doneCh <-chan data.TorrentPiece) error {
	incompleteFiles := client.Torrent.Files
	completedPieces := make(map[int]data.TorrentPiece)

	for _, file := range incompleteFiles {
		// Open the file for writing
		filePath := fmt.Sprintf("%s/%s", outputPath, strings.Join(file.Path, "/"))
		f, err := os.Create(filePath)
		if err != nil {
			return err
		}

		filePieceIndexes := file.PieceIndexes
		currentPiece := 0
		for len(filePieceIndexes) > 0 {
			select {
			case donePiece := <-doneCh:
				// If it is the next piece to be written, write it to the file
				// else, store it
				if filePieceIndexes[currentPiece] == donePiece.Index {
					_, err = f.Write(donePiece.Block)
					if err != nil {
						return err
					}

					currentPiece += 1
					if donePiece.Index == filePieceIndexes[len(filePieceIndexes)-1] {
						continue
					}
				}

				completedPieces[donePiece.Index] = donePiece
			default:
				// Same logic as above, except we check already downloaded pieces
				if piece, ok := completedPieces[currentPiece]; ok {
					if piece.Index != filePieceIndexes[len(filePieceIndexes)-1] {
						delete(completedPieces, piece.Index)
					}
				}
			}
		}

		// Close the file as the download is complete
		f.Close()
	}

	return nil
}
*/

// ? Put client.Torrent.Pieces behind a RWMutex? (maybe fine since we only alert peers after we write the piece to the file)
func (client *Client) fileWriterRoutine(outputPath string, writtenCh chan<- int64, doneCh <-chan data.DoneChStruct) error {
	piecesLeft := len(client.Torrent.Pieces)
	filePath := fmt.Sprintf("%s/temp.part", outputPath)
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	fileOffset := int64(0)
	for piecesLeft > 0 {
		select {
		case donePieceStruct := <-doneCh:
			log.Printf("Writing piece %d\n", donePieceStruct.Index)
			donePiece := client.Torrent.Pieces[donePieceStruct.Index]
			// Write the piece to the file
			f.Write(donePieceStruct.Buf)
			client.Torrent.Pieces[donePiece.Index].FileOffset = uint32(fileOffset)
			fileOffset += int64(donePiece.Length)
			piecesLeft -= 1
			// Indicate the the piece has been written and ready to be read by peers
			writtenCh <- int64(donePiece.Index)
		default:
		}
	}

	err = client.splitFiles(outputPath)
	if err != nil {
		return err
	}
	return nil
}

func (client *Client) splitFiles(outputPath string) error {
	log.Println("test")
	tempFile, err := os.Open(fmt.Sprintf("%s/temp.part", outputPath))
	if err != nil {
		return err
	}

	for _, file := range client.Torrent.Files {
		filePath := fmt.Sprintf("%s/%s", outputPath, strings.Join(file.Path, "/"))
		log.Println(filePath)
		newFile, err := os.Create(filePath)
		if err != nil {
			return err
		}

		for _, pieceIndex := range file.PieceIndexes {
			piece := client.Torrent.Pieces[pieceIndex]
			buffer := make([]byte, piece.Length)
			_, err = tempFile.ReadAt(buffer, int64(piece.FileOffset))
			if err != nil {
				return err
			}
			newFile.Write(buffer)
		}
		newFile.Close()
	}
	tempFile.Close()

	err = os.Remove(fmt.Sprintf("%s/temp.part", outputPath))
	if err != nil {
		return err
	}

	return nil
}
