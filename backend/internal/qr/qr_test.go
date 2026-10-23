package qr

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

func bitmap(c *Code) string {
	var b strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Symbols made by libqrencode 4.1.1 (qrencode -t ASCII -m 0 -8 -l <level>
// -v <version> "v<version>"), as the first 16 hex digits
// of the SHA-256 of their rows ('1' dark, '0' light, one line per row). The
// mask choice may differ between encoders: one of the eight must match.
var vectors = []struct {
	level   Level
	version int
	sha     string
}{
	{L, 1, "9b7c413a4d7174db"},
	{L, 2, "60e3edeb2db63f43"},
	{L, 3, "10b3e7f077530f0b"},
	{L, 4, "25b36e3911b3ef85"},
	{L, 5, "58264273fbe4a5be"},
	{L, 6, "bfe681bac94129c2"},
	{L, 7, "f182b70685dbb1aa"},
	{L, 8, "a14c0a95005fce97"},
	{L, 9, "7f46152fdcb9950d"},
	{L, 10, "3097b1dce5da79ef"},
	{L, 11, "f083e7cee08818d5"},
	{L, 12, "1d5eb074b5c10101"},
	{L, 13, "2abea732a5b5a284"},
	{L, 14, "51a8c3abab65fcdf"},
	{L, 15, "3ad90383bb013bdf"},
	{L, 16, "6a7a13b286145362"},
	{L, 17, "91256e1654ec32c3"},
	{L, 18, "bbe07be642da8200"},
	{L, 19, "9bbffca539bdf871"},
	{L, 20, "0d55604271975535"},
	{L, 21, "9fbe00368b44cf6b"},
	{L, 22, "9bd41ace55d384ef"},
	{L, 23, "2fdd1980dbd6ca2a"},
	{L, 24, "82bfe721c155a866"},
	{L, 25, "79729861a295c480"},
	{L, 26, "28c0b5885250c228"},
	{L, 27, "4fff99f6dd35cb91"},
	{L, 28, "aed1e584e84b7351"},
	{L, 29, "5e33fea2648d076c"},
	{L, 30, "72234d18fd6432b6"},
	{L, 31, "20d9fd051603678c"},
	{L, 32, "49dde140ec7aa2c2"},
	{L, 33, "8bf9ddd9ecccc959"},
	{L, 34, "7acebfc31fa41439"},
	{L, 35, "8d86c6eb46ac4a33"},
	{L, 36, "30b9283b8d771d42"},
	{L, 37, "4de2c9915379fbd9"},
	{L, 38, "e7a94e0bc661564e"},
	{L, 39, "c86afe932a9a7935"},
	{L, 40, "b3af99a163b51a0f"},
	{M, 1, "22f851267067db4f"},
	{M, 2, "2ac93e7a340f4f41"},
	{M, 3, "c4a71a632319e4e0"},
	{M, 4, "3211378995c39fa2"},
	{M, 5, "b2d18da1280aec88"},
	{M, 6, "d282dced4c6a32c0"},
	{M, 7, "1f85046b157639aa"},
	{M, 8, "18f0660e0b2628fb"},
	{M, 9, "b6b1b462b3c8a5ee"},
	{M, 10, "5d59af41587f6c7b"},
	{M, 11, "8e5d5ef01977bb0c"},
	{M, 12, "3a718642a165279d"},
	{M, 13, "a12a8bbd67c6ae77"},
	{M, 14, "398d469d430ae683"},
	{M, 15, "8c91d3651118ca9a"},
	{M, 16, "640d93353b3c7378"},
	{M, 17, "d17cf693bf3caab4"},
	{M, 18, "54e8b7197472dbee"},
	{M, 19, "0fde6dd6c5333067"},
	{M, 20, "9cc9f42b92b2b7c9"},
	{M, 21, "6be81795553ed92a"},
	{M, 22, "0e669c79969de4ab"},
	{M, 23, "636539ad5d8d7720"},
	{M, 24, "78a8785f24834d9d"},
	{M, 25, "4b3548fc0793630a"},
	{M, 26, "ab4ca878d6f594de"},
	{M, 27, "6b3debea21f77dc7"},
	{M, 28, "34a3bb61a2739c24"},
	{M, 29, "51a6d972b7473acc"},
	{M, 30, "eeb608be5a2ca888"},
	{M, 31, "5cae3ee01f38150b"},
	{M, 32, "7feeeb6672daf628"},
	{M, 33, "49941fdd72330b80"},
	{M, 34, "e64c6a8ae1e82395"},
	{M, 35, "010548b2a289b6c6"},
	{M, 36, "13aa8ae399335ec2"},
	{M, 37, "268eb839671668d5"},
	{M, 38, "aeb45ab2797ef88d"},
	{M, 39, "4aadcb26bc2bba5b"},
	{M, 40, "52b43188e4285d24"},
	{Q, 1, "daa23b5f6b77e7e1"},
	{Q, 2, "a1ace9ff6490eb28"},
	{Q, 3, "5d3725ea83832f36"},
	{Q, 4, "afb811ab84cc78ba"},
	{Q, 5, "415d353e1697721b"},
	{Q, 6, "352191384ca606bf"},
	{Q, 7, "91b8132982becd0d"},
	{Q, 8, "5f6581fcf4699fff"},
	{Q, 9, "25286add95fbe671"},
	{Q, 10, "a1e448490106b1be"},
	{Q, 11, "35b40ac120e055f2"},
	{Q, 12, "43e1a341c214d7a3"},
	{Q, 13, "232e07d16f5e62b4"},
	{Q, 14, "b742612abe577e7d"},
	{Q, 15, "1acf18dd5fb2cc3e"},
	{Q, 16, "62256183bd0de80e"},
	{Q, 17, "9059b76c161b797e"},
	{Q, 18, "29425ebb0eaf39b3"},
	{Q, 19, "164bd590dd56a7c7"},
	{Q, 20, "c9a6e0cca3807217"},
	{Q, 21, "72f2ecd777fa8702"},
	{Q, 22, "49d1dab07e6789ce"},
	{Q, 23, "c49b0f8dccfb7ca7"},
	{Q, 24, "71ba8b5720b3edba"},
	{Q, 25, "c1bc357b7ae369a5"},
	{Q, 26, "2c54205778d4bcc6"},
	{Q, 27, "80c98054ebc6ff50"},
	{Q, 28, "69d46e31175dbb38"},
	{Q, 29, "4cf2ea11b1cb1c8b"},
	{Q, 30, "1c1ee6e3792f01c2"},
	{Q, 31, "5ba1663b3f3fb3da"},
	{Q, 32, "a6b0cfcdb31fe65b"},
	{Q, 33, "42e6619cd079f66b"},
	{Q, 34, "37163b82d9a7c899"},
	{Q, 35, "e849d4f8d6a0702a"},
	{Q, 36, "533dfc23372ce8b2"},
	{Q, 37, "fee39b846e64375c"},
	{Q, 38, "c2e433691fe13331"},
	{Q, 39, "197e25551d12e938"},
	{Q, 40, "f71fab5e9c1f6510"},
	{H, 1, "67e82a1b51bec0e9"},
	{H, 2, "9799e17568aac72e"},
	{H, 3, "af85b72befaac2a1"},
	{H, 4, "e4d1de7c80bd6c08"},
	{H, 5, "f95c2ee1e81a10f5"},
	{H, 6, "26ed8f839dfe4d8b"},
	{H, 7, "c8a78db0e55a4b4a"},
	{H, 8, "470e611e7afe9d3b"},
	{H, 9, "28944c99c87f2ab4"},
	{H, 10, "9fdd3e836b4fa16a"},
	{H, 11, "ee9a70caada13462"},
	{H, 12, "269c611d8dc6371e"},
	{H, 13, "7dca93f0338b4911"},
	{H, 14, "33bc0f3cfa0cfb5c"},
	{H, 15, "3116263e09fd555b"},
	{H, 16, "997a83a1042a7ca2"},
	{H, 17, "d44c1b2edd684615"},
	{H, 18, "2956338b4c31a01b"},
	{H, 19, "697894215a24d5fd"},
	{H, 20, "9fb08caab6572de0"},
	{H, 21, "70fd3f41c2a16084"},
	{H, 22, "c507cfbc9ab30d7e"},
	{H, 23, "9656f1de17ade81f"},
	{H, 24, "24722ec37f3c5e81"},
	{H, 25, "f5c952d1878f7d6d"},
	{H, 26, "ce65679062ec0f18"},
	{H, 27, "eeb25d7f18979430"},
	{H, 28, "71f56ac88b7a701b"},
	{H, 29, "bdebcb43d52d913d"},
	{H, 30, "69959af6326e0f70"},
	{H, 31, "7fe09bde14fb5ca4"},
	{H, 32, "52a954a593ae1024"},
	{H, 33, "e33d64c8282854eb"},
	{H, 34, "2691b61009071e63"},
	{H, 35, "c6ebecf6e53bf83d"},
	{H, 36, "32e15dec3665e542"},
	{H, 37, "b180eea0aa2c7b9c"},
	{H, 38, "170e8bc123336d1c"},
	{H, 39, "4fd05839c08e925e"},
	{H, 40, "da04ef306cadd884"},
}

func TestKnownSymbols(t *testing.T) {
	for _, v := range vectors {
		data := []byte("v" + strconv.Itoa(v.version))
		found := false
		for m := 0; m < 8 && !found; m++ {
			c, err := encode(data, v.level, v.version, m)
			if err != nil {
				t.Fatal(err)
			}
			if c.Version != v.version || c.Size != v.version*4+17 {
				t.Fatalf("version %d: got %d (size %d)", v.version, c.Version, c.Size)
			}
			h := sha256.Sum256([]byte(bitmap(c)))
			found = hex.EncodeToString(h[:])[:16] == v.sha
		}
		if !found {
			t.Errorf("level %d version %d: no mask gives the reference symbol", v.level, v.version)
		}
	}
}

func TestSmallestVersion(t *testing.T) {
	for _, tc := range []struct {
		data    string
		level   Level
		version int
	}{
		{"http://192.168.1.20:7070", M, 2}, // 24 bytes: version 2-M holds 26
		{"https://192.168.100.200:7443", M, 3},
		{strings.Repeat("a", 17), L, 1},
		{strings.Repeat("a", 18), L, 2},
		{strings.Repeat("a", 2953), L, 40},
	} {
		c, err := Encode([]byte(tc.data), tc.level)
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != tc.version {
			t.Errorf("%d bytes at level %d: version %d, want %d", len(tc.data), tc.level, c.Version, tc.version)
		}
	}
	if _, err := Encode(make([]byte, 2954), L); err != ErrTooLong {
		t.Errorf("2954 bytes: %v, want ErrTooLong", err)
	}
}

// The automatic mask is the one of lowest penalty, and the quiet zone is light.
func TestAutoMask(t *testing.T) {
	data := []byte("http://10.0.2.15:7070")
	c, err := Encode(data, M)
	if err != nil {
		t.Fatal(err)
	}
	for m := 0; m < 8; m++ {
		o, _ := encode(data, M, 1, m)
		if o.penaltyOf() < c.penaltyOf() {
			t.Errorf("mask %d scores %d, below the chosen mask %d (%d)", m, o.penaltyOf(), c.Mask, c.penaltyOf())
		}
	}
	if c.Black(-1, 0) || c.Black(0, c.Size) {
		t.Error("modules outside the symbol must be light")
	}
}

func (c *Code) penaltyOf() int { return c.penalty() }
