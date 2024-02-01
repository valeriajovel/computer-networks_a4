package data

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type msgID uint8

const (
	//proto = "BitTorrent protocol"
	proto        = "\x13BitTorrent protocol\x00\x00\x00\x00\x00\x00\x00\x00"
	headerLen    = len(proto) // should be 28
	handshakeLen = 49 + 19
)

const (
	Choke        msgID = 0
	Unchoke      msgID = 1
	Interested   msgID = 2
	Uninterested msgID = 3
	Have         msgID = 4
	Bitfield     msgID = 5
	Request      msgID = 6
	Piece        msgID = 7
	Cancel       msgID = 8
)

type Peer struct {
	Peer_id        string
	Ip             net.IP
	Port           uint16
	Connection     net.Conn
	Torrent        *TorrentMetadata
	Haves          big.Int
	HavesLen       uint32
	Bitfield       []byte
	Bytes          uint32
	Bmu            sync.Mutex
	AmChoking      bool
	AmInterested   bool
	PeerChoking    bool
	PeerInterested bool
	IsNeeded       bool
	IsDownloader   bool
	R2sCh          chan Message
	HaveCh         chan uint32
}

type Message struct {
	ID   msgID
	Data []byte
}

// NewPeer creates a new Peer instance with the given connection.
func (peer *Peer) initializePeer(connection net.Conn, torrentMetadata *TorrentMetadata) error {
	peer.Connection = connection

	splitAddress := strings.Split(connection.RemoteAddr().String(), ":")
	ipAddress := net.ParseIP(splitAddress[0])
	port, err := strconv.ParseUint(splitAddress[len(splitAddress)-1], 10, 16)
	if err != nil {
		return err
	}
	peer.Ip = ipAddress
	peer.Port = uint16(port)

	peer.Torrent = torrentMetadata

	peer.HavesLen = uint32(torrentMetadata.NumPieces)

	peer.AmChoking = true
	peer.AmInterested = false
	peer.PeerChoking = true
	peer.PeerInterested = false

	peer.IsNeeded = true
	peer.IsDownloader = false

	return nil
}

// ConnectToPeer establishes a connection to the given peer address.
func (peer *Peer) ConnectToPeer(torrentMetadata *TorrentMetadata) error {
	peerAddress := fmt.Sprintf("%s:%d", peer.Ip.String(), peer.Port)
	connection, err := net.Dial("tcp", peerAddress)
	if err != nil {
		return err
	}
	err = peer.initializePeer(connection, torrentMetadata)
	if err != nil {
		return err
	}

	err = peer.establishHandshake()
	if err != nil {
		peer.Connection.Close()
		return err
	}

	return nil
}

func (peer *Peer) establishHandshake() error {

	// Send handshake to peer
	handshake := peer.createHandshake()
	_, err := peer.Connection.Write(handshake)
	if err != nil {
		return err
	}

	// Receive handshake from peer
	receivedHandshake, err := peer.receiveHandshake()
	if err != nil {
		return err
	}

	if err := peer.parseHandshake(receivedHandshake); err != nil {
		return err
	}

	return nil
}

func (peer *Peer) createHandshake() []byte {
	var handshake [handshakeLen]byte // should be 68 bytes

	// Place proto in the handshake buffer
	copy(handshake[:headerLen], []byte(proto))
	// Place infoHash in the next bytes
	copy(handshake[headerLen:headerLen+20], []byte(peer.Torrent.Info_hash))
	// Place peerID after infoHash
	copy(handshake[headerLen+20:], []byte(peer.Peer_id))

	return handshake[:]
}

func (peer *Peer) receiveHandshake() ([]byte, error) {
	recvHandshake := make([]byte, handshakeLen)

	_, err := peer.Connection.Read(recvHandshake)

	if err != nil {
		return nil, err
	}

	return recvHandshake, nil
}

func (peer *Peer) parseHandshake(handshake []byte) error {
	// Check the length byte
	if len(handshake) != handshakeLen {
		return fmt.Errorf("Invalid handshake length; expected(%d), got(%d)", handshakeLen, len(handshake))
	}
	// Check the protocol header
	if !bytes.Equal(handshake[:20], []byte(proto[:20])) {
		return fmt.Errorf("Invalid protocol ID; expected(%s), got(%s)", proto, string(handshake[1:20]))
	}
	// Check for valid Info_hash
	if !bytes.Equal(handshake[28:48], []byte(peer.Torrent.Info_hash)) {
		return fmt.Errorf("Invalid Info_hash; expected(%s), got(%s)", peer.Torrent.Info_hash, string(handshake[28:48]))
	}
	//Check for valid Peer ID
	//if peer.Peer_id != "" && !bytes.Equal(handshake[48:], []byte(peer.Peer_id)) {
	//return fmt.Errorf("Invalid Peer ID; expected(%s), got(%s)", peer.Peer_id, string(handshake[48:]))
	//} else {
	peer.Peer_id = string(handshake[48:])
	//}
	return nil
}

// parseMessage continuously reads and handles messages from the peer.
func (peer *Peer) parseMessage() error {
	msgLen, err := peer.readMessage(4)
	if err != nil {
		return fmt.Errorf("ERROR reading message length")
	}

	len := binary.BigEndian.Uint32(msgLen)

	// Check if keep-alive msg
	if len != 0 {

		msg, err := peer.readMessage(int(len))
		if err != nil {
			return fmt.Errorf("ERROR reading message type")
		}

		switch msgID(msg[0]) {

		case Choke:
			peer.PeerChoking = true
		case Unchoke:
			peer.PeerChoking = false
		case Interested:
			peer.handleInterested()
		case Uninterested:
			peer.handleUninterested()
		case Have:
			peer.handleHave(binary.BigEndian.Uint32(msg[1:]))
		case Bitfield:
			peer.handleBitfield(msg[1:])
		case Request:
			peer.handleRequest(msg[1:])
		case Piece:
			r2sTodo, err := peer.handlePiece(msg[1:])
			if err == nil {
				peer.R2sCh <- r2sTodo
			}
		case Cancel:
			peer.handleCancel()
		default:
			return fmt.Errorf("ERROR: wrong message type")
		}
	}
	return nil
}

// readMessageType reads and returns the type of the next message.
func (peer *Peer) readMessage(n int) ([]byte, error) {
	msg := make([]byte, 0)
	remaining := n
	for remaining > 0 {
		temp := make([]byte, 1)
		n, err := peer.Connection.Read(temp)
		if err != nil {
			return nil, err
		}
		remaining -= n
		msg = append(msg, temp...)
	}
	return msg, nil
}

// handle interested message
func (peer *Peer) handleInterested() {
	peer.PeerInterested = true
}

// handle uninterested message
func (peer *Peer) handleUninterested() {
	peer.PeerInterested = false
}

// handle have message
func (peer *Peer) handleHave(pieceIndex uint32) {
	peer.Haves.SetBit(&peer.Haves, int(pieceIndex), 1)
}

func (peer *Peer) setBitFromArray(bitfieldLength uint32, bitIndex uint32, bit bool) {
	if bit && bitIndex <= bitfieldLength {
		peer.Haves.SetBit(&peer.Haves, int(bitIndex), 1)
	}
}

// handle bitfield message
func (peer *Peer) handleBitfield(bitfieldArray []byte) {
	bitfieldLength := peer.HavesLen

	for index, byteFromArray := range bitfieldArray {
		peer.setBitFromArray(bitfieldLength, uint32(index*8), bool(byteFromArray&(1<<7) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+1), bool(byteFromArray&(1<<6) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+2), bool(byteFromArray&(1<<5) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+3), bool(byteFromArray&(1<<4) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+4), bool(byteFromArray&(1<<3) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+5), bool(byteFromArray&(1<<2) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+6), bool(byteFromArray&(1<<1) != 0))
		peer.setBitFromArray(bitfieldLength, uint32(index*8+7), bool(byteFromArray&(1<<0) != 0))
	}
}

func (peer *Peer) getBitfieldArray() []byte {
	return peer.Haves.Bytes()
}

// handle request
func (peer *Peer) handleRequest(data []byte) error {
	var pieceMessage Message = Message{}
	if peer.AmChoking == false && peer.PeerInterested == true {
		index := binary.BigEndian.Uint32(data[0:4])
		begin := binary.BigEndian.Uint32(data[4:8])
		len := binary.BigEndian.Uint32(data[8:12])

		var pos uint32
		block := make([]byte, len)
		fileOffset := uint32(index)*uint32(peer.Torrent.PieceLength) + uint32(begin)
		for _, file := range peer.Torrent.Files {
			curr := fileOffset + pos

			if curr >= file.Length {
				continue
			}

			if curr < file.Length {
				break
			}

			remainingBlockLength := uint32(len) - pos
			blockReadLength := uint32(math.Min(float64(remainingBlockLength), float64(file.Length)))
			fd, err := os.OpenFile(filepath.Join(file.Path...), os.O_RDONLY, 0600)
			if err != nil {
				return fmt.Errorf("ERROR: OpenFile()")
			}

			defer fd.Close()

			_, seekErr := fd.Seek(int64(curr), io.SeekStart)
			if seekErr != nil {
				return fmt.Errorf("ERROR: Seek()")
			}

			bytesRead, readErr := fd.Read(block[pos : pos+blockReadLength])
			if readErr != nil {
				return fmt.Errorf("ERROR: Read()")
			}
			pos += uint32(bytesRead)
			if pos >= uint32(len) {
				break
			}
		}

		data := make([]byte, 16392)
		binary.BigEndian.PutUint32(data[0:4], uint32(index))
		binary.BigEndian.PutUint32(data[4:8], uint32(begin))
		copy(data[8:], block)

		pieceMessage = Message{ID: Piece, Data: data}
		payload := packMsg(&pieceMessage)
		_, err := peer.Connection.Write(payload)
		if err != nil {
			return fmt.Errorf("ERROR: failed to send")
		}
		return nil

	}

	return fmt.Errorf("ERROR: invalid request")
}

// handle piece
func (peer *Peer) handlePiece(data []byte) (Message, error) {

	return Message{ID: 7, Data: data}, nil
}

// no endgame mode?
func (peer *Peer) handleCancel() {}

func packMsg(msg *Message) []byte {
	len := uint32(len(msg.Data) + 1)
	buffer := make([]byte, len+4)
	binary.BigEndian.PutUint32(buffer[0:4], len)
	buffer[4] = byte(msg.ID)
	copy(buffer[5:], msg.Data)

	return buffer
}

func (peer *Peer) SendChoke() error {
	payload := packMsg(&Message{ID: Choke, Data: make([]byte, 0)})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send choke message")
	}
	return nil
}

func (peer *Peer) SendUnchoke() error {
	payload := packMsg(&Message{ID: Unchoke, Data: make([]byte, 0)})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send unchoke message")
	}
	return nil
}

func (peer *Peer) SendInterested() error {
	peer.AmInterested = true
	payload := packMsg(&Message{ID: Interested, Data: make([]byte, 0)})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send interested message")
	}
	return nil
}

func (peer *Peer) SendNotInterested() error {
	peer.AmInterested = false
	payload := packMsg(&Message{ID: Uninterested, Data: make([]byte, 0)})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send not interested message")
	}
	return nil
}

func (peer *Peer) SendRequest(index uint32, begin uint32, length uint32) error {
	data := make([]byte, 12)
	binary.BigEndian.PutUint32(data[0:4], uint32(index))
	binary.BigEndian.PutUint32(data[4:8], uint32(begin))
	binary.BigEndian.PutUint32(data[8:12], uint32(length))

	payload := packMsg(&Message{ID: Request, Data: data})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send piece message")
	}
	return nil
}

func (peer *Peer) SendPiece(index uint32, begin uint32, payload []byte) error {
	data := make([]byte, 16392)
	binary.BigEndian.PutUint32(data[0:4], uint32(index))
	binary.BigEndian.PutUint32(data[4:8], uint32(begin))
	copy(data[8:], payload)

	piece := packMsg(&Message{ID: Piece, Data: data})
	_, err := peer.Connection.Write(piece)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send")
	}
	return nil
}

func (peer *Peer) SendHave(index uint32) error {
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, uint32(index))

	payload := packMsg(&Message{ID: Have, Data: data})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send have message")
	}
	return nil
}

func (peer *Peer) SendBitfield(bitfield big.Int) error {
	bitfieldArray := bitfield.Bytes()
	payload := packMsg(&Message{ID: Bitfield, Data: bitfieldArray})
	_, err := peer.Connection.Write(payload)
	if err != nil {
		return fmt.Errorf("ERROR: failed to send bitfield message")
	}
	return nil
}

func (peer *Peer) HasPiece(pieceIndex uint32) bool {
	if pieceIndex >= 0 {
		bitsOffset := uint32(pieceIndex % 64)

		return peer.Haves.Bit(int(bitsOffset)) == 1
	}
	return false
}

// findReachable combines the bitfields of peers who are unchoking us
func (peer *Peer) FindReachable(peers []Peer) *big.Int {
	combinedBitfield := new(big.Int)

	// Set bits in the combinedBitfield based on peers who are unchoking us
	for h := 0; h < len(peers); h++ {
		var bitfieldLen uint32 = peers[h].HavesLen
		// Check if the neighbor is unchoking us and has a valid bitfield
		if peers[h].PeerChoking == false && bitfieldLen > 0 {
			// Set bits in combinedBitfield based on the neighbor's bitfield
			for i := uint32(0); i < bitfieldLen; i++ {
				if peers[h].HasPiece(i) {
					peer.setBitFromArray(bitfieldLen, i, true)
				}
			}
		}
	}

	// Set bits in combinedBitfield based on self's bitfield
	// var selfBitfieldLen int = peer.HavesLen
	// for i := 0; i < selfBitfieldLen; i++ {
	// 	if peer.hasPiece(uint32(i)) {
	// 		peer.setBitFromArray(bitfieldLen, uint32(i), 1)
	// 	}
	// }

	return combinedBitfield
}
