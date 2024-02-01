package data

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
)

type TrackerInformation struct {
	Min_interval int64
	Tracker_id   string
	Complete     int64
	Incomplete   int64
	Peers        []Peer
	PeersMutex   sync.RWMutex
}

/**
 *	Given a decoded bencode dictionary from a tracker response, update the corresponding fields in the struct
 *
 * @param
 */
func (trackerInfo *TrackerInformation) UpdateTrackerInformation(tm *TorrentMetadata, response map[string]interface{}) error {
	// Check if the 'min interval' field exists
	if response["min interval"] != nil {
		// Set the 'min interval' field
		trackerInfo.Min_interval = response["min interval"].(int64)
	} else {
		trackerInfo.Min_interval = 0
	}

	// If a tracker id is present, store it
	if response["tracker id"] != nil {
		trackerInfo.Tracker_id = response["tracker id"].(string)
	}

	// Check if the 'complete' field exists, if it does, store it
	if response["complete"] != nil {
		//return fmt.Errorf("Tracker response is missing the \"complete\" field")
		trackerInfo.Complete = response["complete"].(int64)
	} else {
		trackerInfo.Complete = -1
	}

	// Check if the 'incomplete' field exists, if it does, store it
	if response["incomplete"] != nil {
		//	return fmt.Errorf("Tracker response is missing the \"incomplete\" field")
		trackerInfo.Incomplete = response["incomplete"].(int64)
	} else {
		trackerInfo.Incomplete = -1
	}

	// Check if the 'peer' field exists
	if response["peers"] == nil {
		return fmt.Errorf("Tracker response is missing the \"peers\" field")
	}

	// If the peers field is a binary string, parse it as a binary string, else parse it as a bencoded dictionary
	var wg sync.WaitGroup
	connectedPeers := make(chan *Peer, 100)
	// When we add the peers from the tracker response, we don't want to duplicate connections with the same peer
	alreadyConnectedPeers := make(map[string]bool)
	for i := 0; i < len(trackerInfo.Peers); i++ {
		connectedPeer := &trackerInfo.Peers[i]
		peerAddr := fmt.Sprintf("%s:%d", connectedPeer.Ip.String(), connectedPeer.Port)
		alreadyConnectedPeers[peerAddr] = true
	}

	if _, ok := response["peers"].(string); ok {
		peersBinaryString := response["peers"].(string)
		numOfPeers := len(peersBinaryString) / 6

		// BigEndian conversion help from https://gist.github.com/ammario/649d4c0da650162efd404af23e25b86b
		for i := 0; i < numOfPeers; i++ {
			tempPeer := Peer{}
			// Copy the IP address
			ipSlice := []byte(peersBinaryString[i*6 : (i*6)+4])
			tempPeer.Ip = make(net.IP, 4)
			binary.BigEndian.PutUint32(tempPeer.Ip, binary.BigEndian.Uint32(ipSlice))

			// Copy the port
			portSlice := []byte(peersBinaryString[(i*6)+4 : (i*6)+6])
			tempPeer.Port = binary.BigEndian.Uint16(portSlice)

			peerAddr := fmt.Sprintf("%s:%d", tempPeer.Ip.String(), tempPeer.Port)
			// If we're not already conncted, try to connect
			if _, exists := alreadyConnectedPeers[peerAddr]; !exists {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := tempPeer.ConnectToPeer(tm)
					if err != nil {
						log.Printf("Failed to establish connection with peer at: %s:%d [ERROR]: %s\n", tempPeer.Ip.String(), tempPeer.Port, err)
						return
					}
					log.Printf("Successfully connected with peer at: %s:%d", tempPeer.Ip.String(), tempPeer.Port)
					connectedPeers <- &tempPeer
				}()
			}
		}
	} else {
		peerList := response["peers"].([]interface{})
		numOfPeers := len(peerList)

		for i := 0; i < numOfPeers; i++ {
			tempPeer := Peer{}
			peerDict := peerList[i].(map[string]interface{})
			// Copy the peer_id if it exists
			if peerDict["peer id"] != nil {
				tempPeer.Peer_id = peerDict["peer id"].(string)
			}
			// Copy the IP address
			tempPeer.Ip = net.ParseIP(peerDict["ip"].(string))
			// Copy the port
			tempPeer.Port = uint16(peerDict["port"].(int64))

			peerAddr := fmt.Sprintf("%s:%d", tempPeer.Ip.String(), tempPeer.Port)
			// If we're not already conncted, try to connect
			if _, exists := alreadyConnectedPeers[peerAddr]; !exists {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := tempPeer.ConnectToPeer(tm)
					if err != nil {
						if peerDict["peer id"] != nil {
							log.Printf("Failed to establish connection with peer at: %s:%d, peer_id: %s [ERROR]: %s\n", tempPeer.Ip.String(), tempPeer.Port, tempPeer.Peer_id, err)
							return
						} else {
							log.Printf("Failed to establish connection with peer at: %s:%d [ERROR]: %s\n", tempPeer.Ip.String(), tempPeer.Port, err)
							return
						}
					}
					if peerDict["peer id"] != nil {
						log.Printf("Successfully connected with peer at: %s:%d, peer_id: %s", tempPeer.Ip.String(), tempPeer.Port, tempPeer.Peer_id)
					} else {
						log.Printf("Successfully connected with peer at: %s:%d", tempPeer.Ip.String(), tempPeer.Port)
					}
					connectedPeers <- &tempPeer
				}()
			}
		}
	}

	wg.Wait()
	close(connectedPeers)
	//trackerInfo.Peers = make([]Peer, 0)
	trackerInfo.PeersMutex.Lock()
	for i := 0; i < len(connectedPeers); i++ {
		peer := <-connectedPeers

		trackerInfo.Peers = append(trackerInfo.Peers, []Peer{{Connection: peer.Connection, Torrent: peer.Torrent}}...)
		trackerInfo.Peers[len(trackerInfo.Peers)-1].initializePeer(peer.Connection, peer.Torrent)
	}
	trackerInfo.PeersMutex.Unlock()

	return nil
}
