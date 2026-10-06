// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package sniffer

import (
	"bufio"
	"encoding/binary"
)

// TLS record and handshake constants.
//
// Only the fields needed to reach the SNI extension are decoded. Everything
// else is skipped by length, which is what keeps the parser small and keeps it
// from having an opinion about cipher suites it does not need to understand.
const (
	tlsRecordHeaderLen = 5

	// recordTypeHandshake is the record type carrying a ClientHello.
	recordTypeHandshake = 0x16

	// handshakeTypeClientHello identifies the message within the record.
	handshakeTypeClientHello = 0x01

	// extensionServerName is the SNI extension type.
	extensionServerName = 0x0000

	// sniNameTypeHost is the only name type defined for SNI.
	sniNameTypeHost = 0x00
)

// maxTLSRecord bounds a record before it is read.
//
// A TLS record is at most 2^14 bytes of payload plus overhead. A larger length
// is malformed, and reading it would let a peer make the proxy buffer far more
// than the sniff cap allows.
const maxTLSRecord = 1<<14 + 2048

// sniffTLS extracts the SNI from a ClientHello.
//
// Every failure returns an empty result rather than an error: a connection
// whose handshake cannot be parsed is one that will not be named, and the
// handshake still has to reach the destination untouched. Reporting it as an
// error would invite a caller to close a connection that is perfectly valid.
func (s *Sniffer) sniffTLS(br *bufio.Reader) Result {
	// Record header: type(1) version(2) length(2).
	header, err := br.Peek(tlsRecordHeaderLen)
	if err != nil {
		return Result{}
	}
	if header[0] != recordTypeHandshake {
		return Result{}
	}

	// The record version is checked only to reject something that is clearly
	// not TLS. Real clients send 0x0301 or 0x0303 here regardless of the
	// version they negotiate, and a stricter check would refuse them.
	if header[1] != 0x03 {
		return Result{}
	}

	recordLen := int(binary.BigEndian.Uint16(header[3:5]))
	if recordLen <= 0 || recordLen > maxTLSRecord {
		return Result{}
	}

	// Re-slice rather than read: Peek is non-destructive, so the bytes remain
	// buffered for the relay whatever this parse concludes.
	record, err := br.Peek(tlsRecordHeaderLen + recordLen)
	if err != nil {
		// The record is split across TCP segments. Waiting for the rest would
		// block the connection for a name that may not even be present, so the
		// attempt ends here and the bytes are forwarded as they arrive.
		return Result{}
	}
	body := record[tlsRecordHeaderLen:]

	// Handshake header: type(1) length(3).
	if len(body) < 4 {
		return Result{}
	}
	if body[0] != handshakeTypeClientHello {
		// A ServerHello or another handshake message means this is not the
		// client's first flight.
		return Result{}
	}
	helloLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	body = body[4:]
	if helloLen > len(body) {
		return Result{}
	}
	body = body[:helloLen]

	// ClientHello: version(2) random(32) session_id(1+n) cipher_suites(2+n)
	//              compression_methods(1+n) extensions(2+n)
	if len(body) < 2+32+1 {
		return Result{}
	}
	body = body[2+32:]

	sessionIDLen := int(body[0])
	body = body[1:]
	if len(body) < sessionIDLen {
		return Result{}
	}
	body = body[sessionIDLen:]

	if len(body) < 2 {
		return Result{}
	}
	cipherSuitesLen := int(binary.BigEndian.Uint16(body[:2]))
	body = body[2:]
	if len(body) < cipherSuitesLen {
		return Result{}
	}
	body = body[cipherSuitesLen:]

	if len(body) < 1 {
		return Result{}
	}
	compressionLen := int(body[0])
	body = body[1:]
	if len(body) < compressionLen {
		return Result{}
	}
	body = body[compressionLen:]

	// A ClientHello without extensions has no SNI, which is valid for a client
	// that connects by address.
	if len(body) < 2 {
		return Result{}
	}
	extensionsLen := int(binary.BigEndian.Uint16(body[:2]))
	body = body[2:]
	if extensionsLen > len(body) {
		return Result{}
	}
	return parseSNIExtensions(body[:extensionsLen])
}

// parseSNIExtensions walks the extension list looking for the server name.
//
// Extensions are skipped by their declared length. The list is attacker
// controlled, so every length is validated against what remains before it is
// used; a length that runs past the end ends the walk rather than being
// clamped, because a malformed list has no trustworthy continuation.
func parseSNIExtensions(exts []byte) Result {
	for len(exts) >= 4 {
		extType := binary.BigEndian.Uint16(exts[0:2])
		extLen := int(binary.BigEndian.Uint16(exts[2:4]))
		exts = exts[4:]

		if extLen > len(exts) {
			return Result{}
		}
		if extType == extensionServerName {
			// The slice is bounded by the extension's declared length even
			// though parseServerName reads its own length prefix and would
			// truncate anyway. Bounding it here means the name can never be
			// read from bytes belonging to a following extension, which would
			// otherwise let a client choose which domain the rules see.
			if domain := parseServerName(exts[:extLen]); domain != "" {
				return Result{Domain: domain, Protocol: ProtocolTLS}
			}
			// A malformed or empty SNI does not preclude another one, but a
			// valid ClientHello carries at most one, so the walk continues in
			// case a client sent a duplicate this parser can still read.
		}
		exts = exts[extLen:]
	}
	return Result{}
}

// parseServerName reads a ServerNameList and returns the host name.
func parseServerName(data []byte) string {
	// ServerNameList: list_length(2) then entries of type(1) name_length(2) name.
	if len(data) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	data = data[2:]
	if listLen > len(data) {
		return ""
	}
	data = data[:listLen]

	for len(data) >= 3 {
		nameType := data[0]
		nameLen := int(binary.BigEndian.Uint16(data[1:3]))
		data = data[3:]

		if nameLen > len(data) {
			return ""
		}
		if nameType == sniNameTypeHost {
			return sanitizeDomain(string(data[:nameLen]))
		}
		data = data[nameLen:]
	}
	return ""
}
