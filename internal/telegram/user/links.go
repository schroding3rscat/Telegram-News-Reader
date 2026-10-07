package user

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)

type SourceLink struct {
	Original   string
	Username   string
	InviteHash string
	Private    bool
}

func ParseSourceLink(input string) (SourceLink, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return SourceLink{}, errors.New("empty Telegram source")
	}
	original := value
	value = strings.TrimPrefix(value, "@")
	if usernamePattern.MatchString(value) {
		return SourceLink{Original: original, Username: value}, nil
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil {
		return SourceLink{}, errors.New("invalid Telegram URL")
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if host != "t.me" && host != "telegram.me" && host != "telegram.dog" {
		return SourceLink{}, errors.New("only Telegram URLs are supported")
	}

	path := strings.Trim(strings.TrimSpace(u.Path), "/")
	switch {
	case strings.HasPrefix(path, "+"):
		hash := strings.TrimPrefix(path, "+")
		if hash == "" {
			return SourceLink{}, errors.New("empty invite hash")
		}
		return SourceLink{Original: original, InviteHash: hash, Private: true}, nil
	case strings.HasPrefix(path, "joinchat/"):
		hash := strings.TrimPrefix(path, "joinchat/")
		if hash == "" {
			return SourceLink{}, errors.New("empty invite hash")
		}
		return SourceLink{Original: original, InviteHash: hash, Private: true}, nil
	case usernamePattern.MatchString(path):
		return SourceLink{Original: original, Username: path}, nil
	default:
		return SourceLink{}, errors.New("URL must contain a public channel username or invite hash")
	}
}
