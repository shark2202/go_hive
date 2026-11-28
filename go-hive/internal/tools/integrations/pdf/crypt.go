package pdf

// Standard security handler decryption (ISO 32000-1 §7.6): RC4 40/128-bit,
// AES-128 (V4/AESV2) and AES-256 (V5/AESV3, revisions 5 and 6). Upstream
// delegates this to pdfplumber -> pdfminer, so pdf_read's `password` argument
// must actually decrypt rather than only report that the file is locked.
//
// The algorithm follows the reference implementation the Python tool uses
// (pdfminer.pdfdocument.PDFStandardSecurityHandler and its V4/V5 subclasses).
// Passwords are handled as their raw bytes; the R6 SASLprep pass is a no-op
// for ASCII passwords, which is the only form PDF tooling typically emits.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"hash"
)

// pdfPasswordPadding is Algorithm 2's 32-byte padding string.
var pdfPasswordPadding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41,
	0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80,
	0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

type pdfCryptMethod int

const (
	cryptNone pdfCryptMethod = iota
	cryptRC4
	cryptAES128
	cryptAES256
)

type pdfCrypt struct {
	revision        int
	key             []byte
	streamMethod    pdfCryptMethod
	stringMethod    pdfCryptMethod
	cfMethods       map[string]pdfCryptMethod
	encryptMetadata bool
}

// decryptDocument installs a standard security handler on d and decrypts every
// string and stream in place. d.trailer must already be populated.
func (d *pdfDocument) decryptDocument(password string) error {
	encryptValue := d.trailer["Encrypt"]
	if encryptValue == nil {
		return nil
	}
	encryptRef, _ := encryptValue.(pdfRef)
	handler, err := newPDFCrypt(d, encryptValue, password)
	if err != nil {
		return err
	}
	for ref, object := range d.objects {
		if ref == encryptRef {
			// The encryption dictionary's O/U/OE/UE/Perms strings are
			// explicitly not encrypted.
			continue
		}
		d.objects[ref] = handler.decryptValue(object, ref)
	}
	return nil
}

func newPDFCrypt(d *pdfDocument, encryptValue any, password string) (*pdfCrypt, error) {
	dict := d.dict(encryptValue)
	if dict == nil || pdfNameOf(dict["Filter"]) != "Standard" {
		return nil, errPDFEncrypted
	}
	version := pdfIntValue(d.resolve(dict["V"]))
	revision := pdfIntValue(d.resolve(dict["R"]))
	handler := &pdfCrypt{
		revision:        revision,
		encryptMetadata: true,
		cfMethods:       map[string]pdfCryptMethod{"Identity": cryptNone},
	}
	if value, ok := d.resolve(dict["EncryptMetadata"]).(bool); ok {
		handler.encryptMetadata = value
	}
	o := pdfByteString(d.resolve(dict["O"]))
	u := pdfByteString(d.resolve(dict["U"]))
	identifier := pdfDocumentID(d.trailer)

	if revision >= 5 || version >= 5 {
		methods, err := pdfCryptFilters(d, dict)
		if err != nil {
			return nil, err
		}
		handler.cfMethods = methods
		streamMethod, stringMethod, err := pdfCryptFilterPair(d, dict, methods)
		if err != nil {
			return nil, err
		}
		handler.streamMethod = streamMethod
		handler.stringMethod = stringMethod
		oe := pdfByteString(d.resolve(dict["OE"]))
		ue := pdfByteString(d.resolve(dict["UE"]))
		key, ok := pdfAES256Key(password, revision, o, u, oe, ue)
		if !ok {
			return nil, errPDFEncrypted
		}
		handler.key = key
		return handler, nil
	}

	// Revisions 2-4: derive the file key from the user password, falling back
	// to the owner password (Algorithm 3.2 and 3.7).
	length := pdfIntValue(d.resolve(dict["Length"]))
	if length == 0 {
		length = 40
	}
	if revision == 4 {
		// pdfminer forces 128-bit keys for the V4 handler.
		length = 128
	}
	p := pdfIntValue(d.resolve(dict["P"]))
	key, ok := pdfLegacyKey(password, revision, length, o, u, p, identifier, handler.encryptMetadata)
	if !ok {
		userPassword, ownerOK := pdfLegacyOwnerPassword(password, revision, length, o)
		if ownerOK {
			key, ok = pdfLegacyKey(string(userPassword), revision, length, o, u, p, identifier, handler.encryptMetadata)
		}
	}
	if !ok {
		return nil, errPDFEncrypted
	}
	handler.key = key

	if version >= 4 {
		methods, err := pdfCryptFilters(d, dict)
		if err != nil {
			return nil, err
		}
		handler.cfMethods = methods
		streamMethod, stringMethod, err := pdfCryptFilterPair(d, dict, methods)
		if err != nil {
			return nil, err
		}
		handler.streamMethod = streamMethod
		handler.stringMethod = stringMethod
	} else {
		handler.streamMethod = cryptRC4
		handler.stringMethod = cryptRC4
	}
	return handler, nil
}

// pdfCryptFilters reads the /CF dictionary into filter-name -> method.
func pdfCryptFilters(d *pdfDocument, dict map[string]any) (map[string]pdfCryptMethod, error) {
	methods := map[string]pdfCryptMethod{"Identity": cryptNone}
	cf := d.dict(dict["CF"])
	if cf == nil {
		return methods, nil
	}
	for name, raw := range cf {
		filter := d.dict(raw)
		if filter == nil {
			continue
		}
		switch pdfNameOf(filter["CFM"]) {
		case "V2":
			methods[name] = cryptRC4
		case "AESV2":
			methods[name] = cryptAES128
		case "AESV3":
			methods[name] = cryptAES256
		case "None":
			methods[name] = cryptNone
		default:
			return nil, errPDFEncrypted
		}
	}
	return methods, nil
}

func pdfCryptFilterPair(d *pdfDocument, dict map[string]any, methods map[string]pdfCryptMethod) (pdfCryptMethod, pdfCryptMethod, error) {
	resolve := func(key string) (pdfCryptMethod, error) {
		name := pdfNameOf(d.resolve(dict[key]))
		if name == "" {
			// ISO 32000-1 default is Identity.
			return cryptNone, nil
		}
		method, ok := methods[name]
		if !ok {
			return cryptNone, errPDFEncrypted
		}
		return method, nil
	}
	streamMethod, err := resolve("StmF")
	if err != nil {
		return cryptNone, cryptNone, err
	}
	stringMethod, err := resolve("StrF")
	if err != nil {
		return cryptNone, cryptNone, err
	}
	return streamMethod, stringMethod, nil
}

func pdfDocumentID(trailer map[string]any) []byte {
	values, ok := trailer["ID"].([]any)
	if !ok || len(values) == 0 {
		return nil
	}
	return pdfByteString(values[0])
}

func pdfByteString(value any) []byte {
	text, _ := value.(string)
	return []byte(text)
}

// pdfDecryptValue replaces every encrypted string and stream body reachable
// from value. Object keys are names, so dictionaries only recurse into values.
func (c *pdfCrypt) decryptValue(value any, ref pdfRef) any {
	switch typed := value.(type) {
	case string:
		if c.stringMethod == cryptNone {
			return typed
		}
		return string(c.decryptBytes([]byte(typed), ref, c.stringMethod))
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = c.decryptValue(item, ref)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = c.decryptValue(item, ref)
		}
		return out
	case *pdfStream:
		if pdfNameOf(typed.dict["Type"]) == "XRef" {
			// Cross-reference streams are never encrypted.
			return typed
		}
		method := c.streamMethodFor(typed)
		raw := typed.raw
		if method != cryptNone {
			raw = c.decryptBytes(raw, ref, method)
		}
		decryptedDict, _ := c.decryptValue(typed.dict, ref).(map[string]any)
		return &pdfStream{dict: decryptedDict, raw: raw}
	default:
		return value
	}
}

// streamMethodFor honours per-stream Crypt filters and the EncryptMetadata
// exception for metadata streams.
func (c *pdfCrypt) streamMethodFor(stream *pdfStream) pdfCryptMethod {
	if !c.encryptMetadata && pdfNameOf(stream.dict["Type"]) == "Metadata" {
		return cryptNone
	}
	if name, ok := pdfStreamCryptFilterName(stream.dict); ok {
		if name == "Identity" {
			return cryptNone
		}
		if method, known := c.cfMethods[name]; known {
			return method
		}
		return cryptNone
	}
	return c.streamMethod
}

// pdfStreamCryptFilterName reports the /Crypt filter selection on a stream
// (Filter [/Crypt ...] plus DecodeParms /Name).
func pdfStreamCryptFilterName(dict map[string]any) (string, bool) {
	filters, ok := dict["Filter"]
	if !ok {
		return "", false
	}
	found := false
	switch typed := filters.(type) {
	case pdfName:
		found = string(typed) == "Crypt"
	case []any:
		for _, item := range typed {
			if name, ok := item.(pdfName); ok && string(name) == "Crypt" {
				found = true
			}
		}
	}
	if !found {
		return "", false
	}
	parms, _ := dict["DecodeParms"].(map[string]any)
	if parms == nil {
		if array, ok := dict["DecodeParms"].([]any); ok && len(array) > 0 {
			parms, _ = array[0].(map[string]any)
		}
	}
	if parms == nil {
		return "Identity", true
	}
	return pdfNameOf(parms["Name"]), true
}

func (c *pdfCrypt) decryptBytes(data []byte, ref pdfRef, method pdfCryptMethod) []byte {
	switch method {
	case cryptRC4:
		cipher, err := rc4.NewCipher(c.objectKey(ref, cryptRC4))
		if err != nil {
			return data
		}
		out := make([]byte, len(data))
		cipher.XORKeyStream(out, data)
		return out
	case cryptAES128, cryptAES256:
		if len(data) < aes.BlockSize || (len(data)-aes.BlockSize)%aes.BlockSize != 0 {
			return data
		}
		key := c.objectKey(ref, method)
		block, err := aes.NewCipher(key)
		if err != nil {
			return data
		}
		out := make([]byte, len(data)-aes.BlockSize)
		cipher.NewCBCDecrypter(block, data[:aes.BlockSize]).CryptBlocks(out, data[aes.BlockSize:])
		return pdfUnpad(out)
	default:
		return data
	}
}

// objectKey implements Algorithms 3.1/3.2a for RC4 and AES-128. AES-256 uses
// the file key directly.
func (c *pdfCrypt) objectKey(ref pdfRef, method pdfCryptMethod) []byte {
	if method == cryptAES256 {
		return c.key
	}
	material := make([]byte, 0, len(c.key)+7)
	material = append(material, c.key...)
	material = append(material,
		byte(ref.num), byte(ref.num>>8), byte(ref.num>>16),
		byte(ref.gen), byte(ref.gen>>8))
	if method == cryptAES128 {
		material = append(material, 's', 'A', 'l', 'T')
	}
	sum := md5.Sum(material)
	length := len(material)
	if length > aes.BlockSize {
		length = aes.BlockSize
	}
	return sum[:length]
}

func pdfPadPassword(password string) []byte {
	padded := make([]byte, 32)
	// Algorithm 2 operates on the password encoded as Latin-1, which is what
	// pdfminer uses for revisions 2-4.
	raw := make([]byte, 0, len(password))
	for _, r := range password {
		if r <= 0xFF {
			raw = append(raw, byte(r))
		} else {
			raw = append(raw, []byte(string(r))...)
		}
	}
	copy(padded, raw)
	if len(raw) < 32 {
		copy(padded[len(raw):], pdfPasswordPadding)
	}
	return padded
}

func pdfLegacyKey(password string, revision, length int, o, u []byte, p int, identifier []byte, encryptMetadata bool) ([]byte, bool) {
	padded := pdfPadPassword(password)
	hash := md5.New()
	hash.Write(padded)
	hash.Write(o)
	var permissions [4]byte
	binary.LittleEndian.PutUint32(permissions[:], uint32(p))
	hash.Write(permissions[:])
	hash.Write(identifier)
	if revision >= 4 && !encryptMetadata {
		hash.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	digest := hash.Sum(nil)
	keyLength := 5
	if revision >= 3 {
		keyLength = length / 8
		if keyLength < 5 {
			keyLength = 5
		}
		if keyLength > 16 {
			keyLength = 16
		}
		for i := 0; i < 50; i++ {
			sum := md5.Sum(digest[:keyLength])
			digest = sum[:]
		}
	}
	key := append([]byte(nil), digest[:keyLength]...)
	if !pdfCheckUserKey(key, revision, u, identifier) {
		return nil, false
	}
	return key, true
}

// pdfCheckUserKey implements Algorithm 3.6.
func pdfCheckUserKey(key []byte, revision int, u, identifier []byte) bool {
	if len(key) == 0 || len(u) == 0 {
		return false
	}
	if revision == 2 {
		return bytes.Equal(pdfRC4(key, pdfPasswordPadding), u)
	}
	hash := md5.New()
	hash.Write(pdfPasswordPadding)
	hash.Write(identifier)
	result := pdfRC4(key, hash.Sum(nil))
	for i := 1; i <= 19; i++ {
		result = pdfRC4(pdfXORKey(key, byte(i)), result)
	}
	return len(u) >= 16 && bytes.Equal(result[:16], u[:16])
}

// pdfLegacyOwnerPassword implements Algorithm 3.7, returning the recovered
// user password bytes.
func pdfLegacyOwnerPassword(password string, revision, length int, o []byte) ([]byte, bool) {
	if len(o) == 0 {
		return nil, false
	}
	sum := md5.Sum(pdfPadPassword(password))
	digest := sum[:]
	keyLength := 5
	if revision >= 3 {
		for i := 0; i < 50; i++ {
			next := md5.Sum(digest)
			digest = next[:]
		}
		keyLength = length / 8
		if keyLength < 5 {
			keyLength = 5
		}
		if keyLength > 16 {
			keyLength = 16
		}
	}
	key := digest[:keyLength]
	var user []byte
	if revision == 2 {
		user = pdfRC4(key, o)
	} else {
		user = append([]byte(nil), o...)
		for i := 19; i >= 0; i-- {
			user = pdfRC4(pdfXORKey(key, byte(i)), user)
		}
	}
	return user, true
}

func pdfAES256Key(password string, revision int, o, u, oe, ue []byte) ([]byte, bool) {
	if len(o) < 48 || len(u) < 48 || len(oe) < 32 || len(ue) < 32 {
		return nil, false
	}
	raw := []byte(password)
	if revision == 6 {
		// SASLprep is an identity transform for ASCII passwords; exotic
		// Unicode passwords are outside this port's behaviour.
		raw = []byte(password)
	}
	if len(raw) > 127 {
		raw = raw[:127]
	}
	if bytes.Equal(pdfPasswordHash(raw, o[32:40], u, revision), o[:32]) {
		intermediate := pdfPasswordHash(raw, o[40:48], u, revision)
		if key, err := pdfAESDecryptNoPad(intermediate, make([]byte, aes.BlockSize), oe[:32]); err == nil {
			return key, true
		}
	}
	if bytes.Equal(pdfPasswordHash(raw, u[32:40], nil, revision), u[:32]) {
		intermediate := pdfPasswordHash(raw, u[40:48], nil, revision)
		if key, err := pdfAESDecryptNoPad(intermediate, make([]byte, aes.BlockSize), ue[:32]); err == nil {
			return key, true
		}
	}
	return nil, false
}

func pdfPasswordHash(password, salt, vector []byte, revision int) []byte {
	if revision == 5 {
		digest := sha256.New()
		digest.Write(password)
		digest.Write(salt)
		digest.Write(vector)
		return digest.Sum(nil)
	}
	return pdfRevision6Hash(password, salt, vector)
}

// pdfRevision6Hash is Algorithm 2.B from ISO 32000-2.
func pdfRevision6Hash(password, salt, vector []byte) []byte {
	digest := sha256.New()
	digest.Write(password)
	digest.Write(salt)
	digest.Write(vector)
	key := digest.Sum(nil)

	var encrypted []byte
	round := 0
	lastByte := 0
	for round < 64 || lastByte > round-32 {
		blockLength := (len(password) + len(key) + len(vector)) * 64
		input := make([]byte, 0, blockLength)
		for i := 0; i < 64; i++ {
			input = append(input, password...)
			input = append(input, key...)
			input = append(input, vector...)
		}
		encrypted = pdfAESEncryptNoPad(key[:16], key[16:32], input)
		if encrypted == nil {
			return key[:32]
		}
		sum := 0
		for _, value := range encrypted[:16] {
			sum += int(value) % 3
		}
		var next hash.Hash
		switch sum % 3 {
		case 0:
			next = sha256.New()
		case 1:
			next = sha512.New384()
		default:
			next = sha512.New()
		}
		next.Write(encrypted)
		// Python keeps the full digest: k is re-hashed at its native length
		// (32/48/64) and only the final key is truncated to 32 bytes.
		key = next.Sum(nil)
		lastByte = int(encrypted[len(encrypted)-1])
		round++
	}
	return key[:32]
}

func pdfAESDecryptNoPad(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data)%aes.BlockSize != 0 {
		return nil, errPDFEncrypted
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return out, nil
}

func pdfAESEncryptNoPad(key, iv, data []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil || len(data)%aes.BlockSize != 0 {
		return nil
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return out
}

func pdfRC4(key, data []byte) []byte {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return nil
	}
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out
}

func pdfXORKey(key []byte, value byte) []byte {
	out := make([]byte, len(key))
	for i, b := range key {
		out[i] = b ^ value
	}
	return out
}

// pdfUnpad strips PKCS#5 padding from decrypted AES blocks. Some producers
// omit the padding; a malformed tail is returned unchanged.
func pdfUnpad(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	padding := int(data[len(data)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(data) {
		return data
	}
	for _, value := range data[len(data)-padding:] {
		if int(value) != padding {
			return data
		}
	}
	return data[:len(data)-padding]
}
