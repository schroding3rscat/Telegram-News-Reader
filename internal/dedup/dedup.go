package dedup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

var (
	urlPattern     = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	mentionPattern = regexp.MustCompile(`(?i)(?:^|\s)@[a-z0-9_]{4,32}\b`)
	spacePattern   = regexp.MustCompile(`\s+`)
)

const (
	simHashBits          = 64
	hashPrefixBytes      = 8
	minSimilarTextLength = 40
)

type Detector struct {
	store         *storage.Store
	window        time.Duration
	textDistance  int
	imageDistance int
}

type Candidate struct {
	Text        string
	MediaUnique []string
	ImageHash   uint64
}

type Match struct {
	Reason      string
	Fingerprint storage.Fingerprint
	DuplicateOf int64
}

func New(store *storage.Store, window time.Duration, textDistance, imageDistance int) *Detector {
	return &Detector{
		store:         store,
		window:        window,
		textDistance:  textDistance,
		imageDistance: imageDistance,
	}
}

func Normalize(text string) string {
	text = strings.ToLower(text)
	text = urlPattern.ReplaceAllString(text, " ")
	text = mentionPattern.ReplaceAllString(text, " ")
	var b strings.Builder
	for _, r := range text {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteByte(' ')
		}
	}
	return strings.TrimSpace(spacePattern.ReplaceAllString(b.String(), " "))
}

func ExactHash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func SimHash(normalized string) uint64 {
	words := strings.Fields(normalized)
	if len(words) == 0 {
		return 0
	}
	var weights [simHashBits]int
	for _, token := range words {
		sum := sha256.Sum256([]byte(token))

		hash := binary.LittleEndian.Uint64(sum[:hashPrefixBytes])
		for bit := range simHashBits {
			if hash&(uint64(1)<<bit) != 0 {
				weights[bit]++
			} else {
				weights[bit]--
			}
		}
	}
	var result uint64
	for bit := range weights {
		if weights[bit] >= 0 {
			result |= uint64(1) << bit
		}
	}
	return result
}

func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

func (d *Detector) Check(ctx context.Context, messageID int64, value Candidate) (Match, error) {
	normalized := Normalize(value.Text)
	media := append([]string(nil), value.MediaUnique...)
	sort.Strings(media)
	mediaKey := strings.Join(media, ",")
	fp := storage.Fingerprint{
		MessageID:   messageID,
		TextHash:    ExactHash(normalized),
		TextSimHash: SimHash(normalized),
		ImageHash:   value.ImageHash,
		MediaUnique: mediaKey,
		CreatedAt:   time.Now(),
	}
	recent, err := d.store.RecentFingerprints(ctx, time.Now().Add(-d.window))
	if err != nil {
		return Match{}, err
	}
	for index := range recent {
		old := &recent[index]
		switch {
		case normalized != "" && old.TextHash == fp.TextHash:
			return Match{DuplicateOf: old.MessageID, Reason: "exact_text", Fingerprint: fp}, nil
		case mediaKey != "" && old.MediaUnique == mediaKey:
			return Match{DuplicateOf: old.MessageID, Reason: "exact_media", Fingerprint: fp}, nil
		case len(normalized) >= minSimilarTextLength && old.TextSimHash != 0 &&
			Hamming(old.TextSimHash, fp.TextSimHash) <= d.textDistance:
			if fp.ImageHash == 0 || old.ImageHash == 0 || Hamming(old.ImageHash, fp.ImageHash) <= d.imageDistance {
				return Match{DuplicateOf: old.MessageID, Reason: "similar_text", Fingerprint: fp}, nil
			}
		case fp.ImageHash != 0 && old.ImageHash != 0 && Hamming(old.ImageHash, fp.ImageHash) <= d.imageDistance:
			return Match{DuplicateOf: old.MessageID, Reason: "similar_image", Fingerprint: fp}, nil
		}
	}
	return Match{Fingerprint: fp}, nil
}
