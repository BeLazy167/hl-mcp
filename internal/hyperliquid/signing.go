package hyperliquid

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secpECDSA "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

var (
	domainTypeHash  = keccak([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	agentTypeHash   = keccak([]byte("Agent(string source,bytes32 connectionId)"))
	domainNameHash  = keccak([]byte("Exchange"))
	domainVerHash   = keccak([]byte("1"))
	domainSeparator = buildDomainSeparator()
)

type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V uint8  `json:"v"`
}

type Signer struct {
	privateKey *secp256k1.PrivateKey
	address    string
}

func NewSigner(rawKey string) (*Signer, error) {
	keyText := strings.TrimPrefix(strings.TrimSpace(rawKey), "0x")
	if len(keyText) != 64 {
		return nil, errors.New("HL_PRIVATE_KEY must contain exactly 32 bytes")
	}
	keyBytes, err := hex.DecodeString(keyText)
	if err != nil {
		return nil, errors.New("HL_PRIVATE_KEY must be hexadecimal")
	}
	var scalar secp256k1.ModNScalar
	if overflow := scalar.SetByteSlice(keyBytes); overflow || scalar.IsZero() {
		return nil, errors.New("HL_PRIVATE_KEY is outside the secp256k1 scalar range")
	}
	key := secp256k1.NewPrivateKey(&scalar)
	pub := key.PubKey().SerializeUncompressed()
	addressHash := keccak(pub[1:])
	return &Signer{
		privateKey: key,
		address:    "0x" + hex.EncodeToString(addressHash[12:]),
	}, nil
}

func (s *Signer) Address() string { return s.address }

func (s *Signer) SignAction(action any, vaultAddress *[20]byte, nonce uint64, mainnet bool) (Signature, error) {
	return s.SignActionWithExpiresAfter(action, vaultAddress, nonce, nil, mainnet)
}

// SignActionWithExpiresAfter signs an action with Hyperliquid's optional expiresAfter field.
func (s *Signer) SignActionWithExpiresAfter(
	action any, vaultAddress *[20]byte, nonce uint64, expiresAfter *uint64, mainnet bool,
) (Signature, error) {
	wire, err := marshalAction(action)
	if err != nil {
		return Signature{}, err
	}
	return s.signWireWithExpiresAfter(wire, vaultAddress, nonce, expiresAfter, mainnet)
}

func (s *Signer) signWire(wire []byte, vaultAddress *[20]byte, nonce uint64, mainnet bool) (Signature, error) {
	return s.signWireWithExpiresAfter(wire, vaultAddress, nonce, nil, mainnet)
}

func (s *Signer) signWireWithExpiresAfter(
	wire []byte, vaultAddress *[20]byte, nonce uint64, expiresAfter *uint64, mainnet bool,
) (Signature, error) {
	connectionID := actionHash(wire, vaultAddress, nonce, expiresAfter)
	source := "b"
	if mainnet {
		source = "a"
	}
	digest := typedDataHash(source, connectionID)
	compact := secpECDSA.SignCompact(s.privateKey, digest[:], false)
	if len(compact) != 65 || compact[0] < 27 || compact[0] > 28 {
		return Signature{}, fmt.Errorf("unexpected recovery id %d", compact[0])
	}
	return Signature{
		R: "0x" + new(big.Int).SetBytes(compact[1:33]).Text(16),
		S: "0x" + new(big.Int).SetBytes(compact[33:65]).Text(16),
		V: compact[0],
	}, nil
}

func actionHash(action []byte, vaultAddress *[20]byte, nonce uint64, expiresAfter *uint64) [32]byte {
	payload := make([]byte, 0, len(action)+50)
	payload = append(payload, action...)
	var integer [8]byte
	binary.BigEndian.PutUint64(integer[:], nonce)
	payload = append(payload, integer[:]...)
	if vaultAddress == nil {
		payload = append(payload, 0)
	} else {
		payload = append(payload, 1)
		payload = append(payload, vaultAddress[:]...)
	}
	if expiresAfter != nil {
		payload = append(payload, 0)
		binary.BigEndian.PutUint64(integer[:], *expiresAfter)
		payload = append(payload, integer[:]...)
	}
	return keccak(payload)
}

func typedDataHash(source string, connectionID [32]byte) [32]byte {
	sourceHash := keccak([]byte(source))
	encoded := make([]byte, 0, 96)
	encoded = append(encoded, agentTypeHash[:]...)
	encoded = append(encoded, sourceHash[:]...)
	encoded = append(encoded, connectionID[:]...)
	agentHash := keccak(encoded)
	return keccak([]byte{0x19, 0x01}, domainSeparator[:], agentHash[:])
}

func buildDomainSeparator() [32]byte {
	encoded := make([]byte, 0, 160)
	encoded = append(encoded, domainTypeHash[:]...)
	encoded = append(encoded, domainNameHash[:]...)
	encoded = append(encoded, domainVerHash[:]...)
	var chainID [32]byte
	binary.BigEndian.PutUint64(chainID[24:], 1337)
	encoded = append(encoded, chainID[:]...)
	encoded = append(encoded, make([]byte, 32)...)
	return keccak(encoded)
}

func keccak(parts ...[]byte) [32]byte {
	hash := sha3.NewLegacyKeccak256()
	for _, part := range parts {
		_, _ = hash.Write(part)
	}
	var result [32]byte
	hash.Sum(result[:0])
	return result
}

func parseAddress(value string) ([20]byte, error) {
	var result [20]byte
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || len(raw) != len(result) {
		return result, errors.New("address must contain 20 hexadecimal bytes")
	}
	copy(result[:], raw)
	return result, nil
}
