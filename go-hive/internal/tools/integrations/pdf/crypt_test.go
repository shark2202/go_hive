package pdf

// Password decryption fixtures: real standard-security-handler PDFs written by
// pypdf 6.6.2 (RC4-40, RC4-128, AES-128, AES-256 revision 5 and revision 6),
// each with a two-page text layer, /Title, /Author and the user password
// "hive". They are embedded so the decrypt path is exercised without network
// access or Python at test time; pdfplumber reads every one of them with the
// same password, which is the upstream contract pdf_read mirrors.

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pdfFixtureRC440 = `JVBERi0xLjMKJeLjz9MKMSAwIG9iago8PAovUHJvZHVjZXIgPDAwMWIwYjFlNzI+Ci9UaXRsZSA8MjMwNzE4MDg3MWQ4OTkx
MGE5MmU+Ci9BdXRob3IgPDM4MGIwZDFmPgo+PgplbmRvYmoKMiAwIG9iago8PAovVHlwZSAvUGFnZXMKL0NvdW50IDIKL0tp
ZHMgWyA0IDAgUiA1IDAgUiBdCj4+CmVuZG9iagozIDAgb2JqCjw8Ci9UeXBlIC9DYXRhbG9nCi9QYWdlcyAyIDAgUgo+Pgpl
bmRvYmoKNCAwIG9iago8PAovVHlwZSAvUGFnZQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA2IDAgUgo+Pgo+PgovTWVk
aWFCb3ggWyAwLjAgMC4wIDYxMiA3OTIgXQovUGFyZW50IDIgMCBSCi9Db250ZW50cyA3IDAgUgo+PgplbmRvYmoKNSAwIG9i
ago8PAovVHlwZSAvUGFnZQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA4IDAgUgo+Pgo+PgovTWVkaWFCb3ggWyAwLjAg
MC4wIDYxMiA3OTIgXQovUGFyZW50IDIgMCBSCi9Db250ZW50cyA5IDAgUgo+PgplbmRvYmoKNiAwIG9iago8PAovVHlwZSAv
Rm9udAovU3VidHlwZSAvVHlwZTEKL0Jhc2VGb250IC9IZWx2ZXRpY2EKPj4KZW5kb2JqCjcgMCBvYmoKPDwKL0xlbmd0aCA0
Ngo+PgpzdHJlYW0KmamjbQ4MSsv7PnYtzyaz5FbiSD8+J43Ug1x7jTq+3zxEpN2dVPz+LkZqXZtBRwplbmRzdHJlYW0KZW5k
b2JqCjggMCBvYmoKPDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9i
ago5IDAgb2JqCjw8Ci9MZW5ndGggNDYKPj4Kc3RyZWFtCtVJWUnEzNqnkHPyf+TXeuXOqp7D2Zno4rnY6jGMFmg3aUY8B8gM
TI1b/l1DNh4KZW5kc3RyZWFtCmVuZG9iagoxMCAwIG9iago8PAovViAxCi9SIDIKL0xlbmd0aCA0MAovUCA0Mjk0OTY3Mjky
Ci9GaWx0ZXIgL1N0YW5kYXJkCi9PIDxlNTc4MDBmYzU0MzNiY2RkZjE1ZWJiMjQxZTRiZjBkZDUzMzBkZDkwNWViYmVmOWJh
OGJjMGZkYWUwOGRmZmQxPgovVSA8YTBjMTc3ZDY0ZDc2MGJiNzU1ZjRhYmRkNmRiNDk4NTQ4N2M0ZTczNWJjNjQxOGU4YmNh
MjEyYzA3NzhhYmEzMz4KPj4KZW5kb2JqCnhyZWYKMCAxMQowMDAwMDAwMDAwIDY1NTM1IGYgCjAwMDAwMDAwMTUgMDAwMDAg
biAKMDAwMDAwMDEwOCAwMDAwMCBuIAowMDAwMDAwMTczIDAwMDAwIG4gCjAwMDAwMDAyMjIgMDAwMDAgbiAKMDAwMDAwMDM1
NCAwMDAwMCBuIAowMDAwMDAwNDg2IDAwMDAwIG4gCjAwMDAwMDA1NTYgMDAwMDAgbiAKMDAwMDAwMDY1MiAwMDAwMCBuIAow
MDAwMDAwNzIyIDAwMDAwIG4gCjAwMDAwMDA4MTggMDAwMDAgbiAKdHJhaWxlcgo8PAovU2l6ZSAxMQovUm9vdCAzIDAgUgov
SW5mbyAxIDAgUgovSUQgWyA8NjM2MjM5MzgzMDM3MzIzMTY2MzAzMDMwMzk2MjY0NjI2MzMzNjEzMjM4MzEzNjY0MzUzNzY0
Mzk2MzYzMzYzMz4gPDYzNjIzOTM4MzAzNzMyMzE2NjMwMzAzMDM5NjI2NDYyNjMzMzYxMzIzODMxMzY2NDM1Mzc2NDM5NjM2
MzM2MzM+IF0KL0VuY3J5cHQgMTAgMCBSCj4+CnN0YXJ0eHJlZgoxMDMzCiUlRU9GCg==`

const pdfFixtureRC4128 = `JVBERi0xLjMKJeLjz9MKMSAwIG9iago8PAovUHJvZHVjZXIgPDE2OGFiMTFmOGQ+Ci9UaXRsZSA8MzU5NmEyMDk4ZWUwNDZk
NmZiYTU+Ci9BdXRob3IgPDJlOWFiNzFlPgo+PgplbmRvYmoKMiAwIG9iago8PAovVHlwZSAvUGFnZXMKL0NvdW50IDIKL0tp
ZHMgWyA0IDAgUiA1IDAgUiBdCj4+CmVuZG9iagozIDAgb2JqCjw8Ci9UeXBlIC9DYXRhbG9nCi9QYWdlcyAyIDAgUgo+Pgpl
bmRvYmoKNCAwIG9iago8PAovVHlwZSAvUGFnZQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA2IDAgUgo+Pgo+PgovTWVk
aWFCb3ggWyAwLjAgMC4wIDYxMiA3OTIgXQovUGFyZW50IDIgMCBSCi9Db250ZW50cyA3IDAgUgo+PgplbmRvYmoKNSAwIG9i
ago8PAovVHlwZSAvUGFnZQovUmVzb3VyY2VzIDw8Ci9Gb250IDw8Ci9GMSA4IDAgUgo+Pgo+PgovTWVkaWFCb3ggWyAwLjAg
MC4wIDYxMiA3OTIgXQovUGFyZW50IDIgMCBSCi9Db250ZW50cyA5IDAgUgo+PgplbmRvYmoKNiAwIG9iago8PAovVHlwZSAv
Rm9udAovU3VidHlwZSAvVHlwZTEKL0Jhc2VGb250IC9IZWx2ZXRpY2EKPj4KZW5kb2JqCjcgMCBvYmoKPDwKL0xlbmd0aCA0
Ngo+PgpzdHJlYW0KLQg6gNfejrJg5eTowO0oUQWM/+phLE986EJFoz68m0HRcpIInGUk614a80RlnwplbmRzdHJlYW0KZW5k
b2JqCjggMCBvYmoKPDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9i
ago5IDAgb2JqCjw8Ci9MZW5ndGggNDYKPj4Kc3RyZWFtCi5JIfsA71fk7BmbdLPdvZHib/XVNExSsulipEXUmXxgktjTkDgK
z/uGRTah7aIKZW5kc3RyZWFtCmVuZG9iagoxMCAwIG9iago8PAovViAyCi9SIDMKL0xlbmd0aCAxMjgKL1AgNDI5NDk2NzI5
MgovRmlsdGVyIC9TdGFuZGFyZAovTyA8NzVmZjYzNWQ5ZmE3OThkZGFiNDNiNDViMmMyZGJlNDU2OWExYjk0MjdjMTE2YmIx
NzNmMGZhZWRmZjM2YzQzMz4KL1UgPGU1MTM1NTdmYTY5OWNkNmZkOGE4MDExNmRlMDBjZDk2MjhiZjRlNWU0ZTc1OGE0MTY0
MDA0ZTU2ZmZmYTAxMDg+Cj4+CmVuZG9iagp4cmVmCjAgMTEKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDE1IDAwMDAw
IG4gCjAwMDAwMDAxMDggMDAwMDAgbiAKMDAwMDAwMDE3MyAwMDAwMCBuIAowMDAwMDAwMjIyIDAwMDAwIG4gCjAwMDAwMDAz
NTQgMDAwMDAgbiAKMDAwMDAwMDQ4NiAwMDAwMCBuIAowMDAwMDAwNTU2IDAwMDAwIG4gCjAwMDAwMDA2NTIgMDAwMDAgbiAK
MDAwMDAwMDcyMiAwMDAwMCBuIAowMDAwMDAwODE4IDAwMDAwIG4gCnRyYWlsZXIKPDwKL1NpemUgMTEKL1Jvb3QgMyAwIFIK
L0luZm8gMSAwIFIKL0lEIFsgPDYzNjIzOTM4MzAzNzMyMzE2NjMwMzAzMDM5NjI2NDYyNjMzMzYxMzIzODMxMzY2NDM1Mzc2
NDM5NjM2MzM2MzM+IDw2MzYyMzkzODMwMzczMjMxNjYzMDMwMzAzOTYyNjQ2MjYzMzM2MTMyMzgzMTM2NjQzNTM3NjQzOTYz
NjMzNjMzPiBdCi9FbmNyeXB0IDEwIDAgUgo+PgpzdGFydHhyZWYKMTAzNAolJUVPRgo=`

const pdfFixtureAES128 = `JVBERi0xLjMKJeLjz9MKMSAwIG9iago8PAovUHJvZHVjZXIgPGI5YjUyNWMxOGVlZTVhN2Y5M2ViZmZjODRiYmM2ZGFhZjAw
NzRiOTFkODllZTQ1NmU0YTFhZmRlOTAwMmFmZWI+Ci9UaXRsZSA8ZWRmODQ2MWY5ODA4NzI4OGNiMWRkY2Q5YzQwYmNiMzI5
YmVhYmExOWM5ZThjZDkwMDg2NTI0NjVmMTBlZDljYj4KL0F1dGhvciA8ZTRmM2M4OWNkODZjNGIxYWYwMzQ0Yjg5ZDljNjBh
OWM3N2Q4MDU3OThlZTYwY2Y2YmJiYThmZDM0NGFiOTJiYT4KPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9D
b3VudCAyCi9LaWRzIFsgNCAwIFIgNSAwIFIgXQo+PgplbmRvYmoKMyAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMg
MiAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1Jlc291cmNlcyA8PAovRm9udCA8PAovRjEgNiAwIFIK
Pj4KPj4KL01lZGlhQm94IFsgMC4wIDAuMCA2MTIgNzkyIF0KL1BhcmVudCAyIDAgUgovQ29udGVudHMgNyAwIFIKPj4KZW5k
b2JqCjUgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1Jlc291cmNlcyA8PAovRm9udCA8PAovRjEgOCAwIFIKPj4KPj4KL01lZGlh
Qm94IFsgMC4wIDAuMCA2MTIgNzkyIF0KL1BhcmVudCAyIDAgUgovQ29udGVudHMgOSAwIFIKPj4KZW5kb2JqCjYgMCBvYmoK
PDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago3IDAgb2JqCjw8
Ci9MZW5ndGggNjQKPj4Kc3RyZWFtCg9H3xjFSc1X6xYvZaa8/DjQU0v9a4f99E3v5wb9yiVTCsu6pZGE44E0y6P3ernb5td2
lgm9UxE2rP1zNUwY44kKZW5kc3RyZWFtCmVuZG9iago4IDAgb2JqCjw8Ci9UeXBlIC9Gb250Ci9TdWJ0eXBlIC9UeXBlMQov
QmFzZUZvbnQgL0hlbHZldGljYQo+PgplbmRvYmoKOSAwIG9iago8PAovTGVuZ3RoIDY0Cj4+CnN0cmVhbQo1Oj4hkDw3fI9C
QqDw0Hrmvbk/H8+76O61WCFUxFpyPysF5mBqksBlKxvZiUKUF7DWWMDAWPkIb/rwhUDwT52uCmVuZHN0cmVhbQplbmRvYmoK
MTAgMCBvYmoKPDwKL1YgNAovUiA0Ci9MZW5ndGggMTI4Ci9QIDQyOTQ5NjcyOTIKL0ZpbHRlciAvU3RhbmRhcmQKL08gPDc1
ZmY2MzVkOWZhNzk4ZGRhYjQzYjQ1YjJjMmRiZTQ1NjlhMWI5NDI3YzExNmJiMTczZjBmYWVkZmYzNmM0MzM+Ci9VIDxlNTEz
NTU3ZmE2OTljZDZmZDhhODAxMTZkZTAwY2Q5NjI4YmY0ZTVlNGU3NThhNDE2NDAwNGU1NmZmZmEwMTA4PgovQ0YgPDwKL1N0
ZENGIDw8Ci9BdXRoRXZlbnQgL0RvY09wZW4KL0NGTSAvQUVTVjIKL0xlbmd0aCAxNgo+Pgo+PgovU3RtRiAvU3RkQ0YKL1N0
ckYgL1N0ZENGCj4+CmVuZG9iagp4cmVmCjAgMTEKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDE1IDAwMDAwIG4gCjAw
MDAwMDAyNjIgMDAwMDAgbiAKMDAwMDAwMDMyNyAwMDAwMCBuIAowMDAwMDAwMzc2IDAwMDAwIG4gCjAwMDAwMDA1MDggMDAw
MDAgbiAKMDAwMDAwMDY0MCAwMDAwMCBuIAowMDAwMDAwNzEwIDAwMDAwIG4gCjAwMDAwMDA4MjQgMDAwMDAgbiAKMDAwMDAw
MDg5NCAwMDAwMCBuIAowMDAwMDAxMDA4IDAwMDAwIG4gCnRyYWlsZXIKPDwKL1NpemUgMTEKL1Jvb3QgMyAwIFIKL0luZm8g
MSAwIFIKL0lEIFsgPDYzNjIzOTM4MzAzNzMyMzE2NjMwMzAzMDM5NjI2NDYyNjMzMzYxMzIzODMxMzY2NDM1Mzc2NDM5NjM2
MzM2MzM+IDw2MzYyMzkzODMwMzczMjMxNjYzMDMwMzAzOTYyNjQ2MjYzMzM2MTMyMzgzMTM2NjQzNTM3NjQzOTYzNjMzNjMz
PiBdCi9FbmNyeXB0IDEwIDAgUgo+PgpzdGFydHhyZWYKMTMxNgolJUVPRgo=`

const pdfFixtureAES256R5 = `JVBERi0xLjMKJeLjz9MKMSAwIG9iago8PAovUHJvZHVjZXIgPGExMDdhMGM1Y2FkODBiNTk1NjhjZjkxZTE3ODU0NjI3Y2Y2
NDNkMTFlNWI1MTVlZTMzNjg0Y2NhNjQ1MjdhMzE+Ci9UaXRsZSA8ZTNmMzIxODVjNjViMDcwMjU5NTEyYzcwYWMyNDA5Njg4
MGUwNDI2Nzg1MjA5YjU5MDFkN2Y3NThjYWE0MzkyMT4KL0F1dGhvciA8MmY0ZTlkOWRjZGIxOTM1MzEzYmRiM2RiMWFiZThj
M2Q1N2IwMTg3OWUzZWRkODg3OTNlMDM4MTZlMjk0YTQ5Mj4KPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9D
b3VudCAyCi9LaWRzIFsgNCAwIFIgNSAwIFIgXQo+PgplbmRvYmoKMyAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMg
MiAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1Jlc291cmNlcyA8PAovRm9udCA8PAovRjEgNiAwIFIK
Pj4KPj4KL01lZGlhQm94IFsgMC4wIDAuMCA2MTIgNzkyIF0KL1BhcmVudCAyIDAgUgovQ29udGVudHMgNyAwIFIKPj4KZW5k
b2JqCjUgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1Jlc291cmNlcyA8PAovRm9udCA8PAovRjEgOCAwIFIKPj4KPj4KL01lZGlh
Qm94IFsgMC4wIDAuMCA2MTIgNzkyIF0KL1BhcmVudCAyIDAgUgovQ29udGVudHMgOSAwIFIKPj4KZW5kb2JqCjYgMCBvYmoK
PDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago3IDAgb2JqCjw8
Ci9MZW5ndGggNjQKPj4Kc3RyZWFtCuhI7jHlqwGDAam2msl+y+SAaZUJuhxhyPu4v+vBJvEKsIERsB1N5UAuH9f1jhF694ns
Jq9rhYBVIbwfT0U1KQMKZW5kc3RyZWFtCmVuZG9iago4IDAgb2JqCjw8Ci9UeXBlIC9Gb250Ci9TdWJ0eXBlIC9UeXBlMQov
QmFzZUZvbnQgL0hlbHZldGljYQo+PgplbmRvYmoKOSAwIG9iago8PAovTGVuZ3RoIDY0Cj4+CnN0cmVhbQqfFbESmqyR7da2
q2YprV8N5LScAPZg9wO3G+Z5fA+Hedw10tq/qAypp5wEohDXl9vfk3Mk6YmOwhPOLu3QrNxQCmVuZHN0cmVhbQplbmRvYmoK
MTAgMCBvYmoKPDwKL1YgNQovUiA1Ci9MZW5ndGggMjU2Ci9QIDQyOTQ5NjcyOTIKL0ZpbHRlciAvU3RhbmRhcmQKL08gPDk3
ZmQwMjJhMmUzNmZhNzUxNDFhZDNhZmVmMmYxMWQ5OWRlMjk5ZGIwNjc0YmUxNjg1YTlmYjIxM2VmNGI1YmI1ZTQwZDRkZmM3
NjNkMTg2NTliMDg3ZmE1M2QxMTk0Zj4KL1UgPDA3YmY2ZTQyYjUwMDU0Yzk4OWEyZTc5YTFkN2U3ZGExNDM2MTBmMGMzZGUz
NGRkZTZiZTNmYzYwY2Q5MDFlNDI2YTY1N2I4NDNhZjY3M2Q0OGY1MzYzOTdiYzlkMmVmZD4KL0NGIDw8Ci9TdGRDRiA8PAov
QXV0aEV2ZW50IC9Eb2NPcGVuCi9DRk0gL0FFU1YzCi9MZW5ndGggMzIKPj4KPj4KL1N0bUYgL1N0ZENGCi9TdHJGIC9TdGRD
RgovT0UgPDJmOWU4YzdiMjgyNzU3ZDEzYjA1OTY4ZDk5MDJlMGY0MTljZGI5MDM3ZTIwMTdmMTkwNjQ5YjdkZGQ2NzUzMmU+
Ci9VRSA8ZmY5ZDUyZWUyNTNkZDRjZWNmZjliNWI2NzM1NTU4ZDVjNWE0ZWE5M2E5MTE2YzI3OTk5N2M1NDg1ZDY4MWM2NT4K
L1Blcm1zIDwxNDE5OGMwNDE0MDNmZTU0YTFhMzY4MmZlYjYyMWRmNj4KPj4KZW5kb2JqCnhyZWYKMCAxMQowMDAwMDAwMDAw
IDY1NTM1IGYgCjAwMDAwMDAwMTUgMDAwMDAgbiAKMDAwMDAwMDI2MiAwMDAwMCBuIAowMDAwMDAwMzI3IDAwMDAwIG4gCjAw
MDAwMDAzNzYgMDAwMDAgbiAKMDAwMDAwMDUwOCAwMDAwMCBuIAowMDAwMDAwNjQwIDAwMDAwIG4gCjAwMDAwMDA3MTAgMDAw
MDAgbiAKMDAwMDAwMDgyNCAwMDAwMCBuIAowMDAwMDAwODk0IDAwMDAwIG4gCjAwMDAwMDEwMDggMDAwMDAgbiAKdHJhaWxl
cgo8PAovU2l6ZSAxMQovUm9vdCAzIDAgUgovSW5mbyAxIDAgUgovSUQgWyA8NjM2MjM5MzgzMDM3MzIzMTY2MzAzMDMwMzk2
MjY0NjI2MzMzNjEzMjM4MzEzNjY0MzUzNzY0Mzk2MzYzMzYzMz4gPDYzNjIzOTM4MzAzNzMyMzE2NjMwMzAzMDM5NjI2NDYy
NjMzMzYxMzIzODMxMzY2NDM1Mzc2NDM5NjM2MzM2MzM+IF0KL0VuY3J5cHQgMTAgMCBSCj4+CnN0YXJ0eHJlZgoxNTY0CiUl
RU9GCg==`

const pdfFixtureAES256 = `JVBERi0xLjMKJeLjz9MKMSAwIG9iago8PAovUHJvZHVjZXIgPGJlMDA0MzA3OTM3M2UyZDVlNGJjYWM4ZWQwYjkyMWZiMzUz
NTI2Y2UxNTM1YWNlYzZiOGUxYTE2ZDE0OWY2YzQ+Ci9UaXRsZSA8N2U2ZWM3M2JmNzljMDNiODVkODkzYzc3ZDU2YjVhNGVm
MzhkYjNkM2NhMmVjNDk4ZGU4YWFlY2U4YjkxYTEzNz4KL0F1dGhvciA8ODY1ODYzODcxNTk2YzdkYjAwNWUxNWY1NzA4M2Y5
NjRjYTRiMDJjMWY1MDY1NzA1N2I3ZjU0NzhmZTA1YjYyYT4KPj4KZW5kb2JqCjIgMCBvYmoKPDwKL1R5cGUgL1BhZ2VzCi9D
b3VudCAyCi9LaWRzIFsgNCAwIFIgNSAwIFIgXQo+PgplbmRvYmoKMyAwIG9iago8PAovVHlwZSAvQ2F0YWxvZwovUGFnZXMg
MiAwIFIKPj4KZW5kb2JqCjQgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1Jlc291cmNlcyA8PAovRm9udCA8PAovRjEgNiAwIFIK
Pj4KPj4KL01lZGlhQm94IFsgMC4wIDAuMCA2MTIgNzkyIF0KL1BhcmVudCAyIDAgUgovQ29udGVudHMgNyAwIFIKPj4KZW5k
b2JqCjUgMCBvYmoKPDwKL1R5cGUgL1BhZ2UKL1Jlc291cmNlcyA8PAovRm9udCA8PAovRjEgOCAwIFIKPj4KPj4KL01lZGlh
Qm94IFsgMC4wIDAuMCA2MTIgNzkyIF0KL1BhcmVudCAyIDAgUgovQ29udGVudHMgOSAwIFIKPj4KZW5kb2JqCjYgMCBvYmoK
PDwKL1R5cGUgL0ZvbnQKL1N1YnR5cGUgL1R5cGUxCi9CYXNlRm9udCAvSGVsdmV0aWNhCj4+CmVuZG9iago3IDAgb2JqCjw8
Ci9MZW5ndGggNjQKPj4Kc3RyZWFtClXRsWgPfv+A9pPjNV3RX3q1hTWp9i2S3Epg6foTMhKYRYDrU9ui6R4FKN+FMYYa9r9Y
s5XtPvnhfdLfKepHNs0KZW5kc3RyZWFtCmVuZG9iago4IDAgb2JqCjw8Ci9UeXBlIC9Gb250Ci9TdWJ0eXBlIC9UeXBlMQov
QmFzZUZvbnQgL0hlbHZldGljYQo+PgplbmRvYmoKOSAwIG9iago8PAovTGVuZ3RoIDY0Cj4+CnN0cmVhbQosh0I/Qzi+JoO0
2AL3sfKa/b2QqAzeaIGc8gedqNTW9mZ7uYUtZARgd1wJ8Z/HBTF9WLCV+8lQ8/eOY+dc7xKlCmVuZHN0cmVhbQplbmRvYmoK
MTAgMCBvYmoKPDwKL1YgNQovUiA2Ci9MZW5ndGggMjU2Ci9QIDQyOTQ5NjcyOTIKL0ZpbHRlciAvU3RhbmRhcmQKL08gPDMw
YTdkYjYzYWFiOWM1NTMzNTJmOTc5ODg3MWY2NTBhNTI2MzRlMWYwNDE1NjRjODExMWIwZjRkNWNiOWFiOTUzMmIxNjY4ZTVi
MjNjZjVjY2IzNzFkY2Y2NzNiYmYzNj4KL1UgPGQyNTU5ODk3ODdiY2RkMjE4NjU1N2FkOGVjOTMwYzhjMGM0YTc4MzA4NzA3
MTI3YTRjMDRhMDdlYmRiYzg5OTYyZDVhZTcwZDIyY2E5MDg3ZTk5OWMzM2Q1YTMyNDkxYz4KL0NGIDw8Ci9TdGRDRiA8PAov
QXV0aEV2ZW50IC9Eb2NPcGVuCi9DRk0gL0FFU1YzCi9MZW5ndGggMzIKPj4KPj4KL1N0bUYgL1N0ZENGCi9TdHJGIC9TdGRD
RgovT0UgPDVkNzI4NWQwZTcwMGFlN2MyYjNiMTU3OTUzMGI2MTljYjJjM2EyMmU0NjY0ZWQ5ZjIzMDJkN2NmZTU3NTFlNzA+
Ci9VRSA8YTg3YWM3NjljNzE2NjY1NDdmN2IwOWZiMDg1NWFiNGQ0ZjM3Mjk5ZGNjMGUzNGM0OTJiMzFlMDhkOWI0OTM2Nz4K
L1Blcm1zIDw5OTc3YzY4N2NlNDMzYmM0MzNiZDNjNmU2ZTM1ZGJhZD4KPj4KZW5kb2JqCnhyZWYKMCAxMQowMDAwMDAwMDAw
IDY1NTM1IGYgCjAwMDAwMDAwMTUgMDAwMDAgbiAKMDAwMDAwMDI2MiAwMDAwMCBuIAowMDAwMDAwMzI3IDAwMDAwIG4gCjAw
MDAwMDAzNzYgMDAwMDAgbiAKMDAwMDAwMDUwOCAwMDAwMCBuIAowMDAwMDAwNjQwIDAwMDAwIG4gCjAwMDAwMDA3MTAgMDAw
MDAgbiAKMDAwMDAwMDgyNCAwMDAwMCBuIAowMDAwMDAwODk0IDAwMDAwIG4gCjAwMDAwMDEwMDggMDAwMDAgbiAKdHJhaWxl
cgo8PAovU2l6ZSAxMQovUm9vdCAzIDAgUgovSW5mbyAxIDAgUgovSUQgWyA8NjM2MjM5MzgzMDM3MzIzMTY2MzAzMDMwMzk2
MjY0NjI2MzMzNjEzMjM4MzEzNjY0MzUzNzY0Mzk2MzYzMzYzMz4gPDYzNjIzOTM4MzAzNzMyMzE2NjMwMzAzMDM5NjI2NDYy
NjMzMzYxMzIzODMxMzY2NDM1Mzc2NDM5NjM2MzM2MzM+IF0KL0VuY3J5cHQgMTAgMCBSCj4+CnN0YXJ0eHJlZgoxNTY0CiUl
RU9GCg==`

func pdfFixtureBytes(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return data
}

// Parity: tools/src/aden_tools/tools/pdf_read_tool/pdf_read_tool.py::pdf_read (password is passed to pdfplumber.open)
// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_password_propagated_to_pdfplumber
func TestPDFPasswordDecryption(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{"RC4-40", pdfFixtureRC440},
		{"RC4-128", pdfFixtureRC4128},
		{"AES-128", pdfFixtureAES128},
		{"AES-256-R5", pdfFixtureAES256R5},
		{"AES-256", pdfFixtureAES256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := pdfFixtureBytes(t, tc.fixture)
			doc, err := pdfOpenPassword(data, "hive")
			if err != nil {
				t.Fatalf("open with password: %v", err)
			}
			pages, err := doc.pages()
			if err != nil {
				t.Fatalf("pages: %v", err)
			}
			if len(pages) != 2 {
				t.Fatalf("pages = %d, want 2", len(pages))
			}
			for index, want := range []string{"Secret page one", "Secret page two"} {
				text, err := doc.pageText(pages[index])
				if err != nil {
					t.Fatalf("page %d text: %v", index+1, err)
				}
				if !strings.Contains(text, want) {
					t.Fatalf("page %d text = %q, want %q", index+1, text, want)
				}
			}
			// Info strings live in encrypted objects, so this also proves the
			// per-object key derivation, not just the content streams.
			if title := doc.metadata()["title"]; title != "Secret Doc" {
				t.Fatalf("metadata title = %v", title)
			}
			if _, err := pdfOpenPassword(data, "wrong"); !errors.Is(err, errPDFEncrypted) {
				t.Fatalf("wrong password error = %v, want errPDFEncrypted", err)
			}
			if _, err := pdfOpenPassword(data, ""); !errors.Is(err, errPDFEncrypted) {
				t.Fatalf("missing password error = %v, want errPDFEncrypted", err)
			}
		})
	}
}

// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadTool::test_wrong_password_returns_clear_error
// Parity: tools/tests/tools/test_pdf_read_tool.py::TestPdfReadFailFast::test_encrypted_pdf_yields_with_legacy_error_message
func TestPDFReadPasswordArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.pdf")
	if err := os.WriteFile(path, pdfFixtureBytes(t, pdfFixtureAES128), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	registry := NewRegistry()

	result, err := registry.Execute(context.Background(), "pdf_read", map[string]any{
		"file_path": path,
		"password":  "hive",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", result)
	}
	if _, failed := object["error"]; failed {
		t.Fatalf("password-supplied read failed: %v", object)
	}
	if !strings.Contains(object["content"].(string), "Secret page one") {
		t.Fatalf("content = %v", object["content"])
	}
	// Info strings are encrypted too, so the decrypted /Title proves the
	// per-object key derivation reaches the extraction response, not just
	// the page content streams.
	metadata, _ := object["metadata"].(map[string]any)
	if metadata["title"] != "Secret Doc" {
		t.Fatalf("metadata = %v, want decrypted title", object["metadata"])
	}

	for _, password := range []string{"", "wrong"} {
		input := map[string]any{"file_path": path}
		if password != "" {
			input["password"] = password
		}
		locked, err := registry.Execute(context.Background(), "pdf_read", input)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		entry := locked.(map[string]any)
		message, _ := entry["error"].(string)
		if !strings.Contains(message, "Cannot read encrypted PDF: wrong password or no password supplied.") {
			t.Fatalf("password=%q error = %q", password, message)
		}
		if !strings.Contains(entry["next_step"].(string), "qpdf") {
			t.Fatalf("password=%q next_step = %v", password, entry["next_step"])
		}
		if !strings.Contains(entry["terminal_hint"].(string), "pdftotext") {
			t.Fatalf("password=%q terminal_hint missing", password)
		}
	}
}

// An encrypted PDF whose *user* password is empty (owner password only):
// pdfplumber opens it without a password, so pdf_read must too.
const pdfFixtureOwnerOnly = `JVBERi0xLjMKJeLjz9MKMSAwIG9iago8PAovUHJvZHVjZXIgPDkwOTE4NzZjMTE1ZDNjMDU4OGFhYjk2MmYwZWZjYjMwOTFm
NGE1NDMwNTA4MDZlNDgyZGFjMThjODc4OTQ5ZWQ+Cj4+CmVuZG9iagoyIDAgb2JqCjw8Ci9UeXBlIC9QYWdlcwovQ291bnQg
MQovS2lkcyBbIDQgMCBSIF0KPj4KZW5kb2JqCjMgMCBvYmoKPDwKL1R5cGUgL0NhdGFsb2cKL1BhZ2VzIDIgMCBSCj4+CmVu
ZG9iago0IDAgb2JqCjw8Ci9UeXBlIC9QYWdlCi9SZXNvdXJjZXMgPDwKL0ZvbnQgPDwKL0YxIDUgMCBSCj4+Cj4+Ci9NZWRp
YUJveCBbIDAuMCAwLjAgNjEyIDc5MiBdCi9QYXJlbnQgMiAwIFIKL0NvbnRlbnRzIDYgMCBSCj4+CmVuZG9iago1IDAgb2Jq
Cjw8Ci9UeXBlIC9Gb250Ci9TdWJ0eXBlIC9UeXBlMQovQmFzZUZvbnQgL0hlbHZldGljYQo+PgplbmRvYmoKNiAwIG9iago8
PAovTGVuZ3RoIDgwCj4+CnN0cmVhbQrrL1A5w0WV7nCt0ADQOVzLFZTwMYqTjtyChp/NQZcn8vVXEFseqVy82zOale1MuDs+
2R9eqnmV9RzpjD1nbW8hQ9Wa6Qor57rWujPPWnULqQplbmRzdHJlYW0KZW5kb2JqCjcgMCBvYmoKPDwKL1YgNAovUiA0Ci9M
ZW5ndGggMTI4Ci9QIDQyOTQ5NjcyOTIKL0ZpbHRlciAvU3RhbmRhcmQKL08gPDM1Mjk1YjY2Zjk2ZDVjYzI4MTM2NzA0Y2I3
ZDdmMTFiYjg3NWI4ZmM4MjU3NTU4NzhjOTQ2ZDkzYjQ2OTA0Yjc+Ci9VIDw5Y2I3ZDA3Y2FmZmQ4NjE0ZTIxNWQxZTUwZTZi
ZjZiNDI4YmY0ZTVlNGU3NThhNDE2NDAwNGU1NmZmZmEwMTA4PgovQ0YgPDwKL1N0ZENGIDw8Ci9BdXRoRXZlbnQgL0RvY09w
ZW4KL0NGTSAvQUVTVjIKL0xlbmd0aCAxNgo+Pgo+PgovU3RtRiAvU3RkQ0YKL1N0ckYgL1N0ZENGCj4+CmVuZG9iagp4cmVm
CjAgOAowMDAwMDAwMDAwIDY1NTM1IGYgCjAwMDAwMDAwMTUgMDAwMDAgbiAKMDAwMDAwMDExMyAwMDAwMCBuIAowMDAwMDAw
MTcyIDAwMDAwIG4gCjAwMDAwMDAyMjEgMDAwMDAgbiAKMDAwMDAwMDM1MyAwMDAwMCBuIAowMDAwMDAwNDIzIDAwMDAwIG4g
CjAwMDAwMDA1NTMgMDAwMDAgbiAKdHJhaWxlcgo8PAovU2l6ZSA4Ci9Sb290IDMgMCBSCi9JbmZvIDEgMCBSCi9JRCBbIDwz
NDM4MzczNjYzMzI2NDMzNjQ2NDM4NjYzMzYzMzUzNDM0MzA2NDM4MzQzNzM4Mzc2NjMwMzE2MTM4MzUzMzMwPiA8MzQzODM3
MzY2MzMyNjQzMzY0NjQzODY2MzM2MzM1MzQzNDMwNjQzODM0MzczODM3NjYzMDMxNjEzODM1MzMzMD4gXQovRW5jcnlwdCA3
IDAgUgo+PgpzdGFydHhyZWYKODYwCiUlRU9GCg==`

// Parity: tools/src/aden_tools/tools/pdf_read_tool/pdf_read_tool.py::pdf_read (password or "")
func TestPDFPasswordEmptyUserPassword(t *testing.T) {
	data := pdfFixtureBytes(t, pdfFixtureOwnerOnly)
	doc, err := pdfOpenPassword(data, "")
	if err != nil {
		t.Fatalf("open with empty user password: %v", err)
	}
	pages, err := doc.pages()
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	text, err := doc.pageText(pages[0])
	if err != nil {
		t.Fatalf("text: %v", err)
	}
	if !strings.Contains(text, "Permissions only page") {
		t.Fatalf("text = %q", text)
	}
}
