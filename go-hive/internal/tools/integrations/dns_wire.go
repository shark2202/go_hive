package integrations

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// Minimal DNS wire client used for record types the standard library's
// net.Resolver cannot ask for (CAA, DNSKEY) and for AXFR zone-transfer probes.
// The scanner itself depends on the dnsResolver interface, so tests never need
// this client.

const (
	dnsTypeNS     uint16 = 2
	dnsTypeMX     uint16 = 15
	dnsTypeTXT    uint16 = 16
	dnsTypeDNSKEY uint16 = 48
	dnsTypeAXFR   uint16 = 252
	dnsTypeCAA    uint16 = 257
)

type dnsAnswer struct {
	Type uint16
	Text string
}

type dnsWireClient struct {
	addr    string
	timeout time.Duration
}

// systemDNSServer reads the first nameserver from resolv.conf, falling back to
// a public resolver on platforms without that file.
func systemDNSServer() string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "nameserver" {
				addr := fields[1]
				if strings.Contains(addr, ":") {
					return "[" + addr + "]:53"
				}
				return addr + ":53"
			}
		}
	}
	return "8.8.8.8:53"
}

// dnsBuildQuery encodes a standard query message.
func dnsBuildQuery(id uint16, name string, qtype uint16, recursion bool) []byte {
	message := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(message[0:], id)
	flags := uint16(0)
	if recursion {
		flags = 0x0100
	}
	binary.BigEndian.PutUint16(message[2:], flags)
	binary.BigEndian.PutUint16(message[4:], 1) // qdcount
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" {
			continue
		}
		if len(label) > 63 {
			label = label[:63]
		}
		message = append(message, byte(len(label)))
		message = append(message, label...)
	}
	message = append(message, 0)
	qtypeBytes := make([]byte, 4)
	binary.BigEndian.PutUint16(qtypeBytes[0:], qtype)
	binary.BigEndian.PutUint16(qtypeBytes[2:], 1) // IN
	return append(message, qtypeBytes...)
}

// dnsDecodeName decodes a (possibly compressed) domain name.
func dnsDecodeName(message []byte, offset int) (string, int, error) {
	var labels []string
	cursor := offset
	next := offset
	jumped := false
	for hops := 0; hops < 64; hops++ {
		if cursor >= len(message) {
			return "", 0, errors.New("dns: truncated name")
		}
		length := int(message[cursor])
		switch {
		case length == 0:
			cursor++
			if !jumped {
				next = cursor
			}
			return strings.Join(labels, "."), next, nil
		case length&0xC0 == 0xC0:
			if cursor+1 >= len(message) {
				return "", 0, errors.New("dns: truncated pointer")
			}
			pointer := int(binary.BigEndian.Uint16(message[cursor:]) & 0x3FFF)
			if !jumped {
				next = cursor + 2
				jumped = true
			}
			cursor = pointer
		default:
			if cursor+1+length > len(message) {
				return "", 0, errors.New("dns: truncated label")
			}
			labels = append(labels, string(message[cursor+1:cursor+1+length]))
			cursor += 1 + length
		}
	}
	return "", 0, errors.New("dns: compression loop")
}

// dnsParseMessage parses a response into its rcode and answers.
func dnsParseMessage(message []byte) (int, []uint16, []dnsAnswer, error) {
	if len(message) < 12 {
		return 0, nil, nil, errors.New("dns: short response")
	}
	flags := binary.BigEndian.Uint16(message[2:])
	rcode := int(flags & 0x000F)
	questions := int(binary.BigEndian.Uint16(message[4:]))
	answers := int(binary.BigEndian.Uint16(message[6:]))
	offset := 12
	for index := 0; index < questions; index++ {
		_, next, err := dnsDecodeName(message, offset)
		if err != nil {
			return rcode, nil, nil, err
		}
		offset = next + 4
	}
	parsed := make([]dnsAnswer, 0, answers)
	types := make([]uint16, 0, answers)
	for index := 0; index < answers; index++ {
		_, next, err := dnsDecodeName(message, offset)
		if err != nil {
			return rcode, nil, nil, err
		}
		if next+10 > len(message) {
			return rcode, nil, nil, errors.New("dns: truncated answer")
		}
		recordType := binary.BigEndian.Uint16(message[next:])
		length := int(binary.BigEndian.Uint16(message[next+8:]))
		dataStart := next + 10
		if dataStart+length > len(message) {
			return rcode, nil, nil, errors.New("dns: truncated rdata")
		}
		types = append(types, recordType)
		parsed = append(parsed, dnsAnswer{Type: recordType, Text: dnsRDataText(message, dataStart, length, recordType)})
		offset = dataStart + length
	}
	return rcode, types, parsed, nil
}

// dnsRDataText renders the rdata forms the scanner reports.
func dnsRDataText(message []byte, start, length int, recordType uint16) string {
	data := message[start : start+length]
	switch recordType {
	case dnsTypeTXT:
		var builder strings.Builder
		for cursor := 0; cursor < len(data); {
			size := int(data[cursor])
			cursor++
			if cursor+size > len(data) {
				break
			}
			builder.Write(data[cursor : cursor+size])
			cursor += size
		}
		return builder.String()
	case dnsTypeMX:
		if len(data) < 3 {
			return ""
		}
		preference := binary.BigEndian.Uint16(data)
		name, _, err := dnsDecodeName(message, start+2)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%d %s.", preference, strings.TrimSuffix(name, "."))
	case dnsTypeNS:
		name, _, err := dnsDecodeName(message, start)
		if err != nil {
			return ""
		}
		return name + "."
	case dnsTypeCAA:
		if len(data) < 2 {
			return ""
		}
		flag := data[0]
		tagLength := int(data[1])
		if 2+tagLength > len(data) {
			return ""
		}
		tag := string(data[2 : 2+tagLength])
		value := string(data[2+tagLength:])
		return fmt.Sprintf(`%d %s "%s"`, flag, tag, value)
	default:
		return ""
	}
}

// exchange sends one UDP query and returns the parsed answers.
func (c *dnsWireClient) exchange(ctx context.Context, name string, qtype uint16) ([]dnsAnswer, error) {
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "udp", c.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	id := uint16(time.Now().UnixNano())
	query := dnsBuildQuery(id, name, qtype, true)
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buffer := make([]byte, 4096)
	size, err := conn.Read(buffer)
	if err != nil {
		return nil, err
	}
	message := buffer[:size]
	if len(message) < 2 || binary.BigEndian.Uint16(message) != id {
		return nil, errors.New("dns: mismatched response id")
	}
	rcode, _, answers, err := dnsParseMessage(message)
	if err != nil {
		return nil, err
	}
	if rcode == 3 {
		return nil, errors.New("dns: no such host")
	}
	if rcode != 0 {
		return nil, fmt.Errorf("dns: rcode %d", rcode)
	}
	return answers, nil
}

// zoneTransfer counts records returned by an AXFR request over TCP.
func (c *dnsWireClient) zoneTransfer(ctx context.Context, nameserver, domain string) (int, error) {
	address := nameserver
	if !strings.Contains(address, ":") {
		address = net.JoinHostPort(strings.TrimSuffix(address, "."), "53")
	}
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)

	id := uint16(time.Now().UnixNano())
	query := dnsBuildQuery(id, domain, dnsTypeAXFR, false)
	framed := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(framed, uint16(len(query)))
	copy(framed[2:], query)
	if _, err := conn.Write(framed); err != nil {
		return 0, err
	}

	records := 0
	for messages := 0; messages < 64; messages++ {
		header := make([]byte, 2)
		if _, err := io.ReadFull(conn, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return records, err
		}
		size := int(binary.BigEndian.Uint16(header))
		if size == 0 || size > 65535 {
			break
		}
		message := make([]byte, size)
		if _, err := io.ReadFull(conn, message); err != nil {
			break
		}
		rcode, _, answers, err := dnsParseMessage(message)
		if err != nil {
			break
		}
		if rcode != 0 {
			if records == 0 {
				return 0, fmt.Errorf("dns: axfr rcode %d", rcode)
			}
			break
		}
		records += len(answers)
		if len(answers) == 0 {
			break
		}
	}
	if records > 0 {
		return records, nil
	}
	return 0, errors.New("dns: zone transfer refused")
}

// systemDNSResolver is the production dnsResolver: net.Resolver for the record
// types it supports, the wire client for the rest.
type systemDNSResolver struct {
	wire  *dnsWireClient
	inner *net.Resolver
}

func newSystemDNSResolver() *systemDNSResolver {
	return &systemDNSResolver{
		wire:  &dnsWireClient{addr: systemDNSServer(), timeout: 10 * time.Second},
		inner: net.DefaultResolver,
	}
}

func (r *systemDNSResolver) TXT(ctx context.Context, name string) ([]string, error) {
	return r.inner.LookupTXT(ctx, name)
}

func (r *systemDNSResolver) MX(ctx context.Context, name string) ([]dnsMXRecord, error) {
	records, err := r.inner.LookupMX(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]dnsMXRecord, 0, len(records))
	for _, record := range records {
		out = append(out, dnsMXRecord{Preference: record.Pref, Exchange: record.Host})
	}
	return out, nil
}

func (r *systemDNSResolver) NS(ctx context.Context, name string) ([]string, error) {
	records, err := r.inner.LookupNS(ctx, name)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.Host)
	}
	return out, nil
}

func (r *systemDNSResolver) CAA(ctx context.Context, name string) ([]string, error) {
	answers, err := r.wire.exchange(ctx, name, dnsTypeCAA)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(answers))
	for _, answer := range answers {
		out = append(out, answer.Text)
	}
	return out, nil
}

func (r *systemDNSResolver) DNSKEY(ctx context.Context, name string) (bool, error) {
	answers, err := r.wire.exchange(ctx, name, dnsTypeDNSKEY)
	if err != nil {
		return false, err
	}
	for _, answer := range answers {
		if answer.Type == dnsTypeDNSKEY {
			return true, nil
		}
	}
	return false, nil
}

func (r *systemDNSResolver) ZoneTransfer(ctx context.Context, nameserver, domain string) (int, error) {
	return r.wire.zoneTransfer(ctx, nameserver, domain)
}
