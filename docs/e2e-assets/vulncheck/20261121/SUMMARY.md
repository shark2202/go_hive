# Dependency vulnerability scan

- verdict: **FAIL**
- unique OSV ids: 23; finding events (symbol/package traces): 1386
- browserbridge: 23 unique / 608 events, 2 SBOM module(s), scanner=v1.10.0, DB=2026-11-11T02:51:11Z, mode=source/symbol, go=go1.26.5
- go-hive: 23 unique / 778 events, 13 SBOM module(s), scanner=v1.10.0, DB=2026-11-11T02:51:11Z, mode=source/symbol, go=go1.26.5

| OSV | fixed in | module(s) | summary |
| --- | --- | --- | --- |
| GO-2026-5026 | v1.26.6 | browserbridge, go-hive | Invoking failure to reject ASCII-only Punycode-encoded labels in golang.org/x/net/idna |
| GO-2026-5942 | v1.26.6 | browserbridge, go-hive | Parsing an invalid SVCB or HTTPS RR can panic in golang.org/x/net/dns/dnsmessage |
| GO-2026-5972 | v1.26.6 | browserbridge, go-hive | Enforce maximum recursion depth in encoding/asn1 |
| GO-2026-6088 | v1.26.6 | browserbridge, go-hive | Add recursion depth guard during decode in encoding/xml |
| GO-2026-6089 | v1.26.6 | browserbridge, go-hive | Apply ReadHeaderTimeout when doing unencrypted HTTP/2 check in net/http |
| GO-2026-6090 | v1.26.6 | browserbridge, go-hive | Limit handshake messages we are willing to accept post-handshake in crypto/tls |
| GO-2026-6091 | v1.26.6 | browserbridge, go-hive | Fix Javascript regexp context tracking in html/template |
| GO-2026-6218 | v1.26.6 | browserbridge, go-hive | Avoid quadratic complexity in resolvePath in net/url |
| GO-2026-6599 | v1.26.9 | browserbridge, go-hive | Reset context tracking on consecutive template expressions in html/template |
| GO-2026-6600 | v1.26.9 | browserbridge, go-hive | Recognize yield as regexp preceder keyword in html/template |
| GO-2026-6603 | v1.26.9 | browserbridge, go-hive | HTTP/2 server memory exhaustion due to Trailer headers in net/http |
| GO-2026-6604 | v1.26.9 | browserbridge, go-hive | Root.Mkdir(All) can follow junctions out of the root on Windows in os |
| GO-2026-6605 | v1.26.9 | browserbridge, go-hive | HTTP/1 client connection desynchronization after CONNECT rejection in net/http |
| GO-2026-6607 | v1.26.9 | browserbridge, go-hive | Reject malformed ECH outer extension references in crypto/tls |
| GO-2026-6608 | v1.26.9 | browserbridge, go-hive | Memory limit bypass when parsing MIME headers in net/textproto, mime/multipart |
| GO-2026-6609 | v1.26.9 | browserbridge, go-hive | Lack of limit on size of parsed Range headers in net/http |
| GO-2026-6610 | v1.26.9 | browserbridge, go-hive | HTTP/2 transport accepts malformed framing-related headers in net/http |
| GO-2026-6611 | v1.26.9 | browserbridge, go-hive | Excessive CPU consumption from repeated initial window changes in net/http |
| GO-2026-6612 | v1.26.9 | browserbridge, go-hive | Double flow control refund on HTTP/2 server streams in net/http |
| GO-2026-6613 | v1.26.9 | browserbridge, go-hive | HTTP/1 server connection desynchronization after 2xx CONNECT response in net/http |
| GO-2026-6617 | v1.26.9 | browserbridge, go-hive | HTTP/2 server crash due to HPACK encoder race in net/http |
| GO-2026-6809 | v1.26.10 | browserbridge, go-hive | Distrust compromised intermediate certificates in crypto/x509 |
| GO-2026-6859 | v1.26.11 | browserbridge, go-hive | HTTP/1 server crash due to MaxBytesReader header race in net/http |
