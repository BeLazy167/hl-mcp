package hyperliquid

import (
	"encoding/hex"
	"testing"
)

const vectorPrivateKey = "0x0123456789012345678901234567890123456789012345678901234567890123"

func TestActionHashMatchesOfficialPythonSDK(t *testing.T) {
	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 4, IsBuy: true, Price: "1670.1", Size: "0.0147",
			Type: OrderType{Limit: &LimitOrderType{TIF: "Ioc"}},
		}},
		Grouping: "na",
	}
	wire, err := marshalAction(action)
	if err != nil {
		t.Fatal(err)
	}
	got := actionHash(wire, nil, 1677777606040, nil)
	const want = "0fcbeda5ae3c4950a548021552a4fea2226858c4453571bf3f24ba017eac2908"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("action hash = %x, want %s", got, want)
	}
}

func TestExpiresAfterIsIncludedInActionHashAndSignature(t *testing.T) {
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	action := UpdateLeverageAction{Type: "updateLeverage", Asset: 1, IsCross: true, Leverage: 5}
	expiresAfter := uint64(1700000060000)
	const nonce = uint64(1700000000789)
	wire, err := marshalAction(action)
	if err != nil {
		t.Fatal(err)
	}
	hash := actionHash(wire, nil, nonce, &expiresAfter)
	const wantHash = "a127fa49a9a726b11e5965ce3235c17c72ab9e72da598c9862ebbee7842d97cf"
	if hex.EncodeToString(hash[:]) != wantHash {
		t.Fatalf("action hash = %x, want %s", hash, wantHash)
	}
	signature, err := signer.SignActionWithExpiresAfter(action, nil, nonce, &expiresAfter, true)
	if err != nil {
		t.Fatal(err)
	}
	if signature.R != "0x200e99dfbf2e5e9f9f11a12eaba72af9a3777d7073711978ba62d8cf513d801a" ||
		signature.S != "0x3d27d5294ae7dfb8c725007518b941f65e53468290c4d146eb5873db06c99c28" ||
		signature.V != 28 {
		t.Fatalf("signature = %+v", signature)
	}
}

func TestOrderSignatureMatchesOfficialPythonSDK(t *testing.T) {
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 1, IsBuy: true, Price: "100", Size: "100",
			Type: OrderType{Limit: &LimitOrderType{TIF: "Gtc"}},
		}},
		Grouping: "na",
	}
	signature, err := signer.SignAction(action, nil, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if signature.R != "0xd65369825a9df5d80099e513cce430311d7d26ddf477f5b3a33d2806b100d78e" {
		t.Errorf("r = %s", signature.R)
	}
	if signature.S != "0x2b54116ff64054968aa237c20ca9ff68000f977c93289157748a3162b6ea940e" {
		t.Errorf("s = %s", signature.S)
	}
	if signature.V != 28 {
		t.Errorf("v = %d", signature.V)
	}
}

func TestTriggerSignatureMatchesOfficialPythonSDK(t *testing.T) {
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 1, IsBuy: true, Price: "100", Size: "100",
			Type: OrderType{Trigger: &TriggerOrderType{Trigger: "103", IsMarket: true, TPSL: "sl"}},
		}},
		Grouping: "na",
	}
	signature, err := signer.SignAction(action, nil, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if signature.R != "0x98343f2b5ae8e26bb2587daad3863bc70d8792b09af1841b6fdd530a2065a3f9" {
		t.Errorf("r = %s", signature.R)
	}
	if signature.S != "0x6b5bb6bb0633b710aa22b721dd9dee6d083646a5f8e581a20b545be6c1feb405" {
		t.Errorf("s = %s", signature.S)
	}
	if signature.V != 27 {
		t.Errorf("v = %d", signature.V)
	}
}

func TestVaultSignatureMatchesOfficialPythonSDK(t *testing.T) {
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	packer := new(msgPacker)
	packer.mapLen(2)
	packer.string("type")
	packer.string("dummy")
	packer.string("num")
	packer.uint(100_000_000_000)
	vault, err := parseAddress("0x1719884eb866cb12b2287399b15f7db5e7d775ea")
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signer.signWire(packer.bytes(), &vault, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if signature.R != "0x3c548db75e479f8012acf3000ca3a6b05606bc2ec0c29c50c515066a326239" ||
		signature.S != "0x4d402be7396ce74fbba3795769cda45aec00dc3125a984f2a9f23177b190da2c" || signature.V != 28 {
		t.Fatalf("signature = %+v", signature)
	}
}

func TestCancelSignatureMatchesOfficialRustSDK(t *testing.T) {
	signer, err := NewSigner("e908f86dbb4d55ac876378565aafeabc187f6690f046459397b17d9b9a19688e")
	if err != nil {
		t.Fatal(err)
	}
	action := CancelAction{Type: "cancel", Cancels: []CancelWire{{Asset: 1, OID: 82382}}}
	signature, err := signer.SignAction(action, nil, 1583838, true)
	if err != nil {
		t.Fatal(err)
	}
	if signature.R != "0x2f76cc5b16e0810152fa0e14e7b219f49c361e3325f771544c6f54e157bf9fa" ||
		signature.S != "0x17ed0afc11a98596be85d5cd9f86600aad515337318f7ab346e5ccc1b03425d5" || signature.V != 27 {
		t.Fatalf("signature = %+v", signature)
	}
}

func BenchmarkSignOrder(b *testing.B) {
	signer, err := NewSigner(vectorPrivateKey)
	if err != nil {
		b.Fatal(err)
	}
	action := OrderAction{
		Type: "order",
		Orders: []OrderWire{{
			Asset: 1, IsBuy: true, Price: "100", Size: "100",
			Type: OrderType{Limit: &LimitOrderType{TIF: "Gtc"}},
		}},
		Grouping: "na",
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := signer.SignAction(action, nil, uint64(i), true); err != nil {
			b.Fatal(err)
		}
	}
}
