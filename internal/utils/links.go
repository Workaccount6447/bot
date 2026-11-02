package utils

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math/big"
	"strconv"

	"EverythingSuckz/fsb/config"
)

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewLinkID returns e.g. "k3x9a0qpz2". Starts with a letter (so it can never be
// confused with a legacy numeric message ID) and has no "_" (deep-link separator).
func NewLinkID() string {
	b := make([]byte, 10)
	for i := range b {
		alpha := idAlphabet
		if i == 0 {
			alpha = idAlphabet[:26]
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alpha))))
		b[i] = alpha[n.Int64()]
	}
	return string(b)
}

// LinkHash signs a link ID. MUST match fsb-stream/internal/utils/hashing.go.
func LinkHash(linkID string) string {
	mac := hmac.New(sha256.New, []byte(config.ValueOf.HashSecret))
	mac.Write([]byte("link:" + linkID))
	return hex.EncodeToString(mac.Sum(nil))
}

func ShortHash(full string) string { return full[:config.ValueOf.HashLength] }

func CheckLinkHash(input, linkID string) bool {
	return hmac.Equal([]byte(input), []byte(ShortHash(LinkHash(linkID))))
}

// HostFor picks the stream instance for a key. It is derived from the key itself
// (FNV hash), so the same link always maps to the same host, links spread evenly,
// and no extra state is stored. Any instance can serve any link anyway.
func HostFor(key string) string {
	hosts := config.ValueOf.Hosts
	if len(hosts) == 1 {
		return hosts[0]
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	return hosts[int(h.Sum32()%uint32(len(hosts)))]
}

func StreamURL(linkID string) string {
	return fmt.Sprintf("%s/stream/%s?hash=%s", HostFor(linkID), linkID, ShortHash(LinkHash(linkID)))
}

func DownloadURL(linkID string) string { return StreamURL(linkID) + "&d=true" }

func ShareURL(botUsername, linkID string) string {
	return fmt.Sprintf("https://t.me/%s?start=file_%s_%s", botUsername, linkID, ShortHash(LinkHash(linkID)))
}

// ---- legacy (pre-split) links: /stream/<messageID>?hash=… ------------------------

func PackFileLegacy(fileName string, fileSize int64, mimeType string, fileID int64) string {
	h := md5.New()
	h.Write([]byte(fileName))
	h.Write([]byte(strconv.FormatInt(fileSize, 10)))
	h.Write([]byte(mimeType))
	h.Write([]byte(strconv.FormatInt(fileID, 10)))
	mac := hmac.New(sha256.New, []byte(config.ValueOf.BotToken))
	mac.Write([]byte(hex.EncodeToString(h.Sum(nil))))
	return hex.EncodeToString(mac.Sum(nil))
}

func LegacyStreamURL(messageID int, fullHash string) string {
	return fmt.Sprintf("%s/stream/%d?hash=%s", HostFor(strconv.Itoa(messageID)), messageID, ShortHash(fullHash))
}
