// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js
// +build !js

// ice-tcp demonstrates Pion WebRTC's ICE TCP abilities.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/pion/webrtc/v3"
)

var api *webrtc.API //nolint

type candidatePair struct {
	localID  string
	remoteID string
}

type setCandidateStats struct {
	candidates              []webrtc.ICECandidateStats
	nominatedCandidatePairs []candidatePair
}

func newSetCandidateStats(pc *webrtc.PeerConnection) setCandidateStats {
	stats := pc.GetStats()
	nominatedCandidatePairs := []candidatePair{}
	candidates := []webrtc.ICECandidateStats{}
	for _, v := range stats {
		switch t := v.(type) {
		case webrtc.ICECandidatePairStats:
			candidatePair := candidatePair{}
			if t.Nominated {
				candidatePair.localID = t.LocalCandidateID
				candidatePair.remoteID = t.RemoteCandidateID
				nominatedCandidatePairs = append(nominatedCandidatePairs, candidatePair)
			}
		case webrtc.ICECandidateStats:
			candidates = append(candidates, t)
		}
	}
	return setCandidateStats{
		candidates:              candidates,
		nominatedCandidatePairs: nominatedCandidatePairs,
	}
}

func (scs setCandidateStats) getCandidateStats(id string) webrtc.ICECandidateStats {
	for _, c := range scs.candidates {
		if c.ID == id {
			return c
		}
	}
	return webrtc.ICECandidateStats{}
}

func (scs setCandidateStats) getPriority(lp int32, rp int32) uint64 {
	return uint64((1<<32-1)*math.Min(float64(rp), float64(lp)) + 2*math.Max(float64(rp), float64(lp)))
}

func (scs setCandidateStats) getSetCandidates() []interface{} {
	setCandidates := make([]interface{}, len(scs.nominatedCandidatePairs))
	for _, ncp := range scs.nominatedCandidatePairs {
		fmt.Printf("NCP: %v\n", ncp)
		localCandidate := scs.getCandidateStats(ncp.localID)
		remoteCandidate := scs.getCandidateStats(ncp.remoteID)
		priority := scs.getPriority(localCandidate.Priority, remoteCandidate.Priority)
		setCandidates = append(setCandidates, []interface{}{
			priority,
			localCandidate.NetworkType, localCandidate.Protocol, localCandidate.CandidateType, localCandidate.IP, localCandidate.Port, localCandidate.RelayProtocol,
			remoteCandidate.NetworkType, remoteCandidate.Protocol, remoteCandidate.CandidateType, remoteCandidate.IP, remoteCandidate.Port, remoteCandidate.RelayProtocol,
		})
	}
	return setCandidates
}
func doSignaling(w http.ResponseWriter, r *http.Request) {
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		panic(err)
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
		cps := newSetCandidateStats(peerConnection)
		fmt.Printf("STATS: %v\n", cps.getSetCandidates())

		fmt.Printf("NOMINATED CANDIDATES:\n%v\n", cps.nominatedCandidatePairs)

		fmt.Printf("CANDIDATES: %v\n", cps.candidates)
	})

	// Send the current time via a DataChannel to the remote peer every 3 seconds
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		d.OnOpen(func() {
			for range time.Tick(time.Second * 3) {
				if err = d.SendText(time.Now().String()); err != nil {
					if errors.Is(io.ErrClosedPipe, err) {
						return
					}
					panic(err)
				}
			}
		})
	})

	var offer webrtc.SessionDescription
	if err = json.NewDecoder(r.Body).Decode(&offer); err != nil {
		panic(err)
	}

	if err = peerConnection.SetRemoteDescription(offer); err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	response, err := json.Marshal(*peerConnection.LocalDescription())
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(response); err != nil {
		panic(err)
	}
}

func main() {
	settingEngine := webrtc.SettingEngine{}

	// Enable support only for TCP ICE candidates.
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeTCP4,
		webrtc.NetworkTypeTCP6,
	})

	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{0, 0, 0, 0},
		Port: 8443,
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("Listening for ICE TCP at %s\n", tcpListener.Addr())

	tcpMux := webrtc.NewICETCPMux(nil, tcpListener, 8)
	settingEngine.SetICETCPMux(tcpMux)

	api = webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/doSignaling", doSignaling)

	fmt.Println("Open http://localhost:8080 to access this demo")
	// nolint: gosec
	panic(http.ListenAndServe(":8080", nil))
}
