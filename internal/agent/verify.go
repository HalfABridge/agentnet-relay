package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"sort"

	"github.com/btcsuite/btcutil/base58"
)

// VerifySignature verifies an Ed25519 signature on a message.
// The message is the raw JSON map; signature field is removed before verification.
func VerifySignature(raw json.RawMessage, pubKeyB58, signatureB58 string) bool {
	pubKey := base58.Decode(pubKeyB58)
	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}

	sig := base58.Decode(signatureB58)
	if len(sig) != ed25519.SignatureSize {
		return false
	}

	// Parse, remove signature, canonical serialize
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	delete(m, "signature")

	canonical, err := canonicalJSON(m)
	if err != nil {
		return false
	}

	return ed25519.Verify(pubKey, canonical, sig)
}

// canonicalJSON serializes with sorted keys, no extra whitespace.
func canonicalJSON(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf := []byte{'{'}
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			keyBytes, _ := json.Marshal(k)
			buf = append(buf, keyBytes...)
			buf = append(buf, ':')
			valBytes, err := canonicalJSON(val[k])
			if err != nil {
				return nil, err
			}
			buf = append(buf, valBytes...)
		}
		buf = append(buf, '}')
		return buf, nil
	case []interface{}:
		buf := []byte{'['}
		for i, item := range val {
			if i > 0 {
				buf = append(buf, ',')
			}
			itemBytes, err := canonicalJSON(item)
			if err != nil {
				return nil, err
			}
			buf = append(buf, itemBytes...)
		}
		buf = append(buf, ']')
		return buf, nil
	default:
		return json.Marshal(v)
	}
}
