package openvpn

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const MaxStatusSize = 1 << 20

// Session is the bounded subset of OpenVPN status-version 3 used by the
// administrator. ClientID is only meaningful while the connection is online.
type Session struct {
	Name           string    `json:"name"`
	RealAddress    string    `json:"real_address"`
	VirtualAddress string    `json:"virtual_address"`
	BytesReceived  uint64    `json:"bytes_received"`
	BytesSent      uint64    `json:"bytes_sent"`
	ConnectedSince time.Time `json:"connected_since"`
	ClientID       uint64    `json:"-"`
}

// ParseStatusVersion3 parses the tab-separated CLIENT_LIST records emitted by
// OpenVPN's machine-readable status format.
func ParseStatusVersion3(reader io.Reader) ([]Session, error) {
	limited := io.LimitReader(reader, MaxStatusSize+1)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 128<<10)
	sessions := make([]Session, 0, 10)
	total := 0
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > MaxStatusSize {
			return nil, errors.New("OpenVPN status exceeds size limit")
		}
		if !strings.HasPrefix(line, "CLIENT_LIST\t") {
			continue
		}
		fields, err := parseStatusRecord(line)
		if err != nil {
			return nil, err
		}
		session, err := parseClientList(fields)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
		if len(sessions) > 64 {
			return nil, errors.New("OpenVPN status has too many clients")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read OpenVPN status: %w", err)
	}
	return sessions, nil
}

func parseStatusRecord(line string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false
	fields, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("decode OpenVPN status record: %w", err)
	}
	return fields, nil
}

func parseClientList(fields []string) (Session, error) {
	// OpenVPN 2.6 status-version 3:
	// CLIENT_LIST, Common Name, Real Address, Virtual Address,
	// Virtual IPv6 Address, Bytes Received, Bytes Sent, Connected Since,
	// Connected Since (time_t), Username, Client ID, Peer ID, Cipher.
	if len(fields) < 12 || fields[0] != "CLIENT_LIST" {
		return Session{}, errors.New("OpenVPN CLIENT_LIST record is truncated")
	}
	if fields[1] == "" || len(fields[1]) > 64 {
		return Session{}, errors.New("OpenVPN client name is invalid")
	}
	received, err := strconv.ParseUint(fields[5], 10, 64)
	if err != nil {
		return Session{}, fmt.Errorf("parse received byte count: %w", err)
	}
	sent, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		return Session{}, fmt.Errorf("parse sent byte count: %w", err)
	}
	connectedUnix, err := strconv.ParseInt(fields[8], 10, 64)
	if err != nil || connectedUnix < 0 {
		return Session{}, errors.New("parse connection time")
	}
	clientID, err := strconv.ParseUint(fields[10], 10, 64)
	if err != nil {
		return Session{}, fmt.Errorf("parse client ID: %w", err)
	}
	return Session{
		Name:           fields[1],
		RealAddress:    fields[2],
		VirtualAddress: fields[3],
		BytesReceived:  received,
		BytesSent:      sent,
		ConnectedSince: time.Unix(connectedUnix, 0).UTC(),
		ClientID:       clientID,
	}, nil
}
