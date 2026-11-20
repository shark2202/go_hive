package integrations

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hostResolver resolves a hostname to one IP for the port scanner.
type hostResolver func(ctx context.Context, host string) (string, error)

// portProbe performs one TCP connect probe and returns open + banner.
type portProbe func(ctx context.Context, address string, timeout time.Duration) (bool, string)

// portServiceMap mirrors PORT_SERVICE_MAP.
var portServiceMap = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS",
	80: "HTTP", 110: "POP3", 143: "IMAP", 443: "HTTPS", 445: "SMB",
	993: "IMAPS", 995: "POP3S", 1433: "MSSQL", 3306: "MySQL", 3389: "RDP",
	5432: "PostgreSQL", 5900: "VNC", 6379: "Redis", 8080: "HTTP-Alt", 8443: "HTTPS-Alt",
}

// top20Ports mirrors TOP20_PORTS (sorted PORT_SERVICE_MAP keys).
var top20Ports = []int{21, 22, 23, 25, 53, 80, 110, 143, 443, 445, 993, 995, 1433, 3306, 3389, 5432, 5900, 6379, 8080, 8443}

// top100Ports mirrors TOP100_PORTS: TOP20 plus the extra well-known probes
// (95 entries upstream, despite the name).
var top100Ports = []int{
	8, 20, 21, 22, 23, 25, 53, 69, 80, 110, 111, 119, 123, 135, 137, 138, 139, 143, 161, 162,
	179, 389, 443, 445, 465, 514, 515, 520, 587, 631, 636, 873, 902, 989, 990, 993, 995, 1080,
	1194, 1433, 1443, 1521, 1723, 2049, 2082, 2083, 2086, 2087, 2096, 2181, 2222, 3000, 3128,
	3306, 3389, 4443, 5000, 5001, 5060, 5222, 5432, 5601, 5900, 5984, 6379, 6443, 6660, 6661,
	6662, 6663, 6664, 6665, 6666, 6667, 7001, 7002, 7443, 8000, 8008, 8080, 8081, 8082, 8083,
	8088, 8443, 8888, 9000, 9090, 9200, 9300, 9443, 10000, 11211, 27017, 27018,
}

var databasePorts = map[int]bool{1433: true, 3306: true, 5432: true, 6379: true, 27017: true, 27018: true, 9200: true, 9300: true, 5984: true, 11211: true}
var adminPorts = map[int]bool{3389: true, 5900: true, 2082: true, 2083: true, 2086: true, 2087: true, 10000: true}
var legacyPorts = map[int]bool{21: true, 23: true, 110: true, 143: true, 445: true}

// portFinding mirrors PORT_FINDINGS.
func portFinding(category string) (string, string) {
	switch category {
	case "database":
		return "high", "Restrict database ports to localhost or VPN only. Use firewall rules to block public access."
	case "admin":
		return "high", "Restrict remote admin ports to VPN or trusted IP ranges. Never expose RDP/VNC directly to the internet."
	default:
		return "medium", "Replace legacy protocols with secure alternatives. Use SFTP instead of FTP, SSH instead of Telnet, IMAPS/POP3S instead of IMAP/POP3."
	}
}

func defaultHostResolver(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	addresses, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addresses) == 0 {
		if err == nil {
			err = fmt.Errorf("no addresses for %s", host)
		}
		return "", err
	}
	return addresses[0], nil
}

func defaultPortProbe(ctx context.Context, address string, timeout time.Duration) (bool, string) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false, ""
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 256)
	read, _ := conn.Read(buffer)
	return true, strings.TrimSpace(string(buffer[:read]))
}

// parsePortList mirrors the upstream "top20"/"top100"/csv parsing.
func parsePortList(raw string) ([]int, bool) {
	switch raw {
	case "", "top20":
		return append([]int(nil), top20Ports...), true
	case "top100":
		return append([]int(nil), top100Ports...), true
	}
	seen := map[int]bool{}
	ports := []int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		if !seen[value] {
			seen[value] = true
			ports = append(ports, value)
		}
	}
	sort.Ints(ports)
	return ports, true
}

func portScan(ctx context.Context, _ *Runtime, input map[string]any, resolve hostResolver, probe portProbe) (any, error) {
	hostname := cleanScanTarget(stringArg(input, "hostname"))
	if hostname == "" {
		return map[string]any{"error": "hostname is required"}, nil
	}
	if resolve == nil {
		resolve = defaultHostResolver
	}
	if probe == nil {
		probe = defaultPortProbe
	}
	timeout := floatArg(input, "timeout", 3.0)
	if timeout > 10 {
		timeout = 10
	}
	if timeout <= 0 {
		// Python's asyncio.wait_for(timeout=0) fails immediately; a tiny
		// deadline keeps that behaviour while still bounding the dial.
		timeout = 0.001
	}
	portList, ok := parsePortList(strings.TrimSpace(stringArg(input, "ports")))
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Invalid port list: %s. Use 'top20', 'top100', or '80,443'", stringArg(input, "ports"))}, nil
	}
	ip, err := resolve(ctx, hostname)
	if err != nil {
		return map[string]any{"error": "Could not resolve hostname: " + hostname}, nil
	}

	type probeResult struct {
		port   int
		open   bool
		banner string
	}
	results := make(chan probeResult, len(portList))
	semaphore := make(chan struct{}, 20)
	var group sync.WaitGroup
	for _, port := range portList {
		group.Add(1)
		go func(port int) {
			defer group.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			open, banner := probe(ctx, net.JoinHostPort(ip, strconv.Itoa(port)), time.Duration(timeout*float64(time.Second)))
			results <- probeResult{port: port, open: open, banner: banner}
		}(port)
	}
	group.Wait()
	close(results)

	openPorts := []map[string]any{}
	closedPorts := []int{}
	for result := range results {
		if !result.open {
			closedPorts = append(closedPorts, result.port)
			continue
		}
		service := portServiceMap[result.port]
		if service == "" {
			service = "unknown"
		}
		entry := map[string]any{"port": result.port, "service": service, "banner": result.banner}
		switch {
		case databasePorts[result.port]:
			severity, remediation := portFinding("database")
			entry["severity"] = severity
			entry["finding"] = fmt.Sprintf("%s port (%d) exposed to internet", service, result.port)
			entry["remediation"] = remediation
		case adminPorts[result.port]:
			severity, remediation := portFinding("admin")
			entry["severity"] = severity
			entry["finding"] = fmt.Sprintf("%s admin port (%d) exposed to internet", service, result.port)
			entry["remediation"] = remediation
		case legacyPorts[result.port]:
			severity, remediation := portFinding("legacy")
			entry["severity"] = severity
			entry["finding"] = fmt.Sprintf("Legacy protocol %s (%d) still active", service, result.port)
			entry["remediation"] = remediation
		}
		openPorts = append(openPorts, entry)
	}
	sort.Slice(openPorts, func(i, j int) bool {
		return openPorts[i]["port"].(int) < openPorts[j]["port"].(int)
	})
	sort.Ints(closedPorts)

	openSet := map[int]bool{}
	for _, entry := range openPorts {
		openSet[entry["port"].(int)] = true
	}
	onlyWeb := true
	for port := range openSet {
		if port != 80 && port != 443 && port != 8080 && port != 8443 {
			onlyWeb = false
			break
		}
	}
	return map[string]any{
		"hostname":      hostname,
		"ip":            ip,
		"ports_scanned": len(portList),
		"open_ports":    openPorts,
		"closed_ports":  closedPorts,
		"grade_input": map[string]any{
			"no_database_ports_exposed": !anyPortIn(openSet, databasePorts),
			"no_admin_ports_exposed":    !anyPortIn(openSet, adminPorts),
			"no_legacy_ports_exposed":   !anyPortIn(openSet, legacyPorts),
			"only_web_ports":            onlyWeb,
		},
	}, nil
}

func anyPortIn(open map[int]bool, risky map[int]bool) bool {
	for port := range open {
		if risky[port] {
			return true
		}
	}
	return false
}
