package user

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gotd/td/tg"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

const (
	maxBotUploadSize = 49 * 1024 * 1024
	privateTempMode  = 0o700
)

type DownloadedFile struct {
	Path string
	Type string
	MIME string
	Name string
}

func (c *Client) DownloadMedia(ctx context.Context, item storage.MediaItem) (DownloadedFile, error) {
	if item.Size > maxBotUploadSize {
		return DownloadedFile{}, fmt.Errorf("media is too large for Bot API upload: %d bytes", item.Size)
	}
	id, err := strconv.ParseInt(item.RemoteID, 10, 64)
	if err != nil {
		return DownloadedFile{}, fmt.Errorf("invalid remote media id: %w", err)
	}
	var location tg.InputFileLocationClass

	switch item.Type {
	case "photo":
		location = &tg.InputPhotoFileLocation{
			ID:            id,
			AccessHash:    item.AccessHash,
			FileReference: item.FileRef,
			ThumbSize:     item.ThumbSize,
		}
	case "video", "voice", "document":
		location = &tg.InputDocumentFileLocation{
			ID:            id,
			AccessHash:    item.AccessHash,
			FileReference: item.FileRef,
			ThumbSize:     "",
		}
	default:
		return DownloadedFile{}, errors.New("unsupported Telegram media type")
	}
	tempDir := filepath.Join(filepath.Dir(c.opts.SessionPath), "tmp")
	if mkdirErr := os.MkdirAll(tempDir, privateTempMode); mkdirErr != nil {
		return DownloadedFile{}, mkdirErr
	}
	file, err := os.CreateTemp(tempDir, "media-*")
	if err != nil {
		return DownloadedFile{}, err
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		return DownloadedFile{}, closeErr
	}
	if _, downloadErr := c.client.Download(location).ToPath(ctx, path); downloadErr != nil {
		_ = os.Remove(path)
		return DownloadedFile{}, fmt.Errorf("download Telegram media: %w", downloadErr)
	}
	name := item.Name
	if name == "" {
		name = filepath.Base(path)
	}
	return DownloadedFile{Path: path, Type: item.Type, MIME: item.MIME, Name: name}, nil
}

func (c *Client) CleanupDownload(file DownloadedFile) {
	if file.Path != "" {
		_ = os.Remove(file.Path)
	}
}
