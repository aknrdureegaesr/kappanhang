package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/nonoo/kappanhang/log"
)

var conn *net.UDPConn

func send(d []byte) {
	_, err := conn.Write(d)
	if err != nil {
		log.Fatal(err)
	}
}

func read() ([]byte, error) {
	err := conn.SetReadDeadline(time.Now().Add(time.Second))
	if err != nil {
		log.Fatal(err)
	}

	b := make([]byte, 1500)
	n, _, err := conn.ReadFromUDP(b)
	if err != nil {
		if err, ok := err.(net.Error); ok && !err.Timeout() {
			log.Fatal(err)
		}
	}
	return b[:n], err
}

func main() {
	log.Init()
	parseArgs()

	hostPort := fmt.Sprint(connectAddress, ":", connectPort)
	log.Print("connecting to ", hostPort)
	raddr, err := net.ResolveUDPAddr("udp", hostPort)
	if err != nil {
		log.Fatal(err)
	}
	conn, err = net.DialUDP("udp", nil, raddr)
	if err != nil {
		log.Fatal(err)
	}

	// Constructing the local session ID by combining the local IP address and port.
	laddr := conn.LocalAddr().(*net.UDPAddr)
	localSID := binary.BigEndian.Uint32(laddr.IP[len(laddr.IP)-4:])<<16 | uint32(laddr.Port&0xffff)
	log.Debugf("using session id %.8x", localSID)

	send([]byte{0x10, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x00, 0x00, 0x00, 0x00})
	// send([]byte{0x15, 0x00, 0x00, 0x00, 0x07, 0x00, 0x01, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x3e, 0x10, 0x00})
	// send([]byte{0x10, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x00, 0x00, 0x00, 0x00})
	// send([]byte{0x15, 0x00, 0x00, 0x00, 0x07, 0x00, 0x02, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x00, 0x00, 0x00, 0x00, 0x00, 0x70, 0x3e, 0x10, 0x00})
	// send([]byte{0x10, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x00, 0x00, 0x00, 0x00})

	var remoteSID uint32
	for {
		r, _ := read()
		if bytes.Equal(r[:8], []byte{0x10, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00}) {
			remoteSID = binary.BigEndian.Uint32(r[8:12])
			break
		}
	}

	log.Debugf("got remote session id %.8x", remoteSID)

	send([]byte{0x10, 0x00, 0x00, 0x00, 0x06, 0x00, 0x01, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), byte(remoteSID >> 24), byte(remoteSID >> 16), byte(remoteSID >> 8), byte(remoteSID)})
	send([]byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), byte(remoteSID >> 24), byte(remoteSID >> 16), byte(remoteSID >> 8), byte(remoteSID),
		0x00, 0x00, 0x00, 0x70, 0x01, 0x00, 0x00, 0x22,
		0x00, 0x00, 0x09, 0x27, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x2b, 0x3f, 0x55, 0x5c, 0x00, 0x00, 0x00, 0x00, // username: beer
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x2b, 0x3f, 0x55, 0x5c, 0x3f, 0x25, 0x77, 0x58, // pass: beerbeer
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x69, 0x63, 0x6f, 0x6d, 0x2d, 0x70, 0x63, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	var sendSeq uint8
	var lastReceivedSeq uint8
	var receivedSeq bool
	var lastPingAt time.Time
	var errCount int
	var randID [4]byte
	for {
		r, err := read()
		if err != nil {
			errCount++
			if errCount > 5 {
				log.Fatal("timeout")
			}
			log.Error("stream break detected")
		}
		errCount = 0

		if len(r) == 96 && bytes.Equal(r[:8], []byte{0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00}) {
			if bytes.Equal(r[48:52], []byte{0xff, 0xff, 0xff, 0xfe}) {
				log.Fatal("invalid user/password")
			} else {
				log.Print("auth ok")
			}
		}
		if len(r) == 21 && bytes.Equal(r[1:6], []byte{0x00, 0x00, 0x00, 0x07, 0x00}) {
			gotSeq := r[6]
			if receivedSeq && lastReceivedSeq+1 != gotSeq {
				log.Error("packet loss detected")
			}
			lastReceivedSeq = gotSeq
			receivedSeq = true

			// Replying to the radio.
			// Example request from radio: 0x00, 0x00, 0x00, 0x00, 0x07, 0x00, 0x1c, 0x0e, 0xe4, 0x35, 0xdd, 0x72, 0xbe, 0xd9, 0xf2, 0x63, 0x00, 0x57, 0x2b, 0x12, 0x00
			// Example answer from PC:     0x15, 0x00, 0x00, 0x00, 0x07, 0x00, 0x1c, 0x0e, 0xbe, 0xd9, 0xf2, 0x63, 0xe4, 0x35, 0xdd, 0x72, 0x01, 0x57, 0x2b, 0x12, 0x00
			send([]byte{0x15, 0x00, 0x00, 0x00, 0x07, 0x00, gotSeq, r[7], byte(remoteSID >> 24), byte(remoteSID >> 16), byte(remoteSID >> 8), byte(remoteSID), byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x01,
				r[17], r[18], r[19], r[20]})
		}
		if len(r) == 16 && bytes.Equal(r[:6], []byte{0x10, 0x00, 0x00, 0x00, 0x00, 0x00}) {
			// Replying to the radio.
			// Example receive: 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x13, 0x00, 0xe4, 0x35, 0xdd, 0x72, 0xbe, 0xd9, 0xf2, 0x63
			// Example answer:  0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x13, 0x00, 0xbe, 0xd9, 0xf2, 0x63, 0xe4, 0x35, 0xdd, 0x72
			gotSeq := r[6]
			send([]byte{0x10, 0x00, 0x00, 0x00, 0x00, 0x00, gotSeq, 0x00, byte(remoteSID >> 24), byte(remoteSID >> 16), byte(remoteSID >> 8), byte(remoteSID), byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID)})
		}

		if time.Since(lastPingAt) >= 100*time.Millisecond {
			// Example request from PC:  0x15, 0x00, 0x00, 0x00, 0x07, 0x00, 0x09, 0x00, 0xbe, 0xd9, 0xf2, 0x63, 0xe4, 0x35, 0xdd, 0x72, 0x00, 0x78, 0x40, 0xf6, 0x02
			// Example reply from radio: 0x00, 0x00, 0x00, 0x00, 0x07, 0x00, 0x09, 0x00, 0xe4, 0x35, 0xdd, 0x72, 0xbe, 0xd9, 0xf2, 0x63, 0x01, 0x78, 0x40, 0xf6, 0x02
			_, err = rand.Read(randID[:])
			if err != nil {
				log.Fatal(err)
			}
			send([]byte{0x15, 0x00, 0x00, 0x00, 0x07, 0x00, sendSeq, 0x00, byte(remoteSID >> 24), byte(remoteSID >> 16), byte(remoteSID >> 8), byte(remoteSID), byte(localSID >> 24), byte(localSID >> 16), byte(localSID >> 8), byte(localSID), 0x01,
				randID[0], randID[1], randID[2], randID[3]})
			sendSeq++
			lastPingAt = time.Now()
		}
	}
}
