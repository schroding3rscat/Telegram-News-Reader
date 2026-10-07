package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"github.com/schroding3rscat/Telegram-News-Reader/internal/storage"
)

type Options struct {
	Auth          auth.UserAuthenticator
	Store         *storage.Store
	Logger        *slog.Logger
	AppHash       string
	SessionPath   string
	AppID         int
	MaxQueueDepth int
}

type Client struct {
	client  *telegram.Client
	manager *peers.Manager
	gaps    *updates.Manager
	ready   chan struct{}
	opts    Options
	once    sync.Once
}

const maxChannelDifferenceConcurrency = 2

func New(opts Options) (*Client, error) {
	if opts.AppID == 0 || opts.AppHash == "" {
		return nil, errors.New("telegram app_id and app_hash are required")
	}
	if opts.Store == nil {
		return nil, errors.New("storage is required")
	}
	if opts.Auth == nil {
		return nil, errors.New("telegram authenticator is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	dispatcher := tg.NewUpdateDispatcher()
	var handler telegram.UpdateHandler
	rawClient := telegram.NewClient(opts.AppID, opts.AppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: opts.SessionPath},
		UpdateHandler: telegram.UpdateHandlerFunc(func(ctx context.Context, update tg.UpdatesClass) error {
			if handler == nil {
				return nil
			}
			return handler.Handle(ctx, update)
		}),
	})
	state := NewUpdateStateStorage(opts.Store.DB())
	manager := peers.Options{Storage: &peers.InmemoryStorage{}}.Build(rawClient.API())
	gaps := updates.New(updates.Config{
		Handler:                         dispatcher,
		Storage:                         state,
		AccessHasher:                    state,
		UserAccessHasher:                state,
		MaxChannelDifferenceConcurrency: maxChannelDifferenceConcurrency,
		OnChannelInaccessible: func(channelID int64) {
			opts.Logger.Warn("Telegram channel became inaccessible", "channel_id", channelID)
		},
	})
	handler = manager.UpdateHook(gaps)

	c := &Client{
		opts:    opts,
		client:  rawClient,
		manager: manager,
		gaps:    gaps,
		ready:   make(chan struct{}),
	}
	dispatcher.OnNewChannelMessage(c.handleChannelMessage)
	return c, nil
}

func (c *Client) Run(ctx context.Context) error {
	return c.client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(c.opts.Auth, auth.SendCodeOptions{})
		if err := c.client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("telegram authentication: %w", err)
		}
		if err := c.manager.Init(ctx); err != nil {
			return fmt.Errorf("initialize Telegram peers: %w", err)
		}
		self, err := c.manager.Self(ctx)
		if err != nil {
			return fmt.Errorf("get Telegram self: %w", err)
		}

		c.once.Do(func() { close(c.ready) })
		return c.gaps.Run(ctx, c.client.API(), self.ID(), updates.AuthOptions{})
	})
}

func (c *Client) WaitReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) ResolveAndJoin(ctx context.Context, sourceID int64, raw string) error {
	if err := c.WaitReady(ctx); err != nil {
		return err
	}
	link, err := ParseSourceLink(raw)
	if err != nil {
		return err
	}
	var peer peers.Peer
	if link.Private {
		peer, err = c.manager.JoinLink(ctx, raw)
	} else {
		peer, err = c.manager.ResolveDomain(ctx, link.Username)
		if err == nil {
			if channel, ok := peer.(peers.Channel); ok && channel.Left() {
				err = channel.Join(ctx)
			}
		}
	}
	if err != nil {
		_ = c.opts.Store.UpdateSourceError(ctx, sourceID, err.Error())
		return err
	}
	channel, ok := peer.(peers.Channel)
	if !ok || !channel.IsBroadcast() {
		sourceErr := errors.New("source is not a Telegram broadcast channel")
		_ = c.opts.Store.UpdateSourceError(ctx, sourceID, sourceErr.Error())
		return sourceErr
	}
	username, _ := channel.Username()
	rawChannel := channel.Raw()
	if updateErr := c.opts.Store.UpdateSourceResolved(ctx, sourceID, channel.ID(), rawChannel.AccessHash,
		channel.VisibleName(), username, link.Private); updateErr != nil {
		return fmt.Errorf("save resolved source: %w", updateErr)
	}
	return nil
}

func (c *Client) handleChannelMessage(ctx context.Context, _ tg.Entities, update *tg.UpdateNewChannelMessage) error {
	message, ok := update.Message.(*tg.Message)
	if !ok || message.Out {
		return nil
	}
	peer, ok := message.PeerID.(*tg.PeerChannel)
	if !ok {
		return nil
	}
	source, err := c.opts.Store.SourceByPeerID(ctx, peer.ChannelID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find source: %w", err)
	}
	if c.opts.MaxQueueDepth > 0 {
		depth, depthErr := c.opts.Store.QueueDepth(ctx)
		if depthErr != nil {
			return fmt.Errorf("check queue depth: %w", depthErr)
		}
		if depth >= c.opts.MaxQueueDepth {
			return fmt.Errorf("pipeline queue limit reached: %d", depth)
		}
	}
	groupedID, _ := message.GetGroupedID()
	media, _ := json.Marshal(extractMedia(message))
	_, err = c.opts.Store.EnqueueMessage(ctx, storage.Message{
		SourceID:       source.ID,
		TelegramChatID: peer.ChannelID,
		TelegramMsgID:  message.ID,
		GroupedID:      groupedID,
		Text:           message.Message,
		Media:          media,
		PublishedAt:    time.Unix(int64(message.Date), 0),
		ReceivedAt:     time.Now(),
	})
	return err
}

func extractMedia(message *tg.Message) []storage.MediaItem {
	media, ok := message.GetMedia()
	if !ok {
		return nil
	}

	switch value := media.(type) {
	case *tg.MessageMediaPhoto:
		photoClass, ok := value.GetPhoto()
		if !ok {
			return nil
		}
		photo, ok := photoClass.(*tg.Photo)
		if !ok {
			return nil
		}
		return []storage.MediaItem{{
			Type:       "photo",
			RemoteID:   strconv.FormatInt(photo.ID, 10),
			AccessHash: photo.AccessHash,
			FileRef:    photo.FileReference,
			ThumbSize:  largestPhotoSize(photo),
		}}
	case *tg.MessageMediaDocument:
		documentClass, ok := value.GetDocument()
		if !ok {
			return nil
		}
		document, ok := documentClass.(*tg.Document)
		if !ok {
			return nil
		}
		kind := "document"
		if value.Video {
			kind = "video"
		} else if value.Voice {
			kind = "voice"
		}
		return []storage.MediaItem{{
			Type:       kind,
			RemoteID:   strconv.FormatInt(document.ID, 10),
			AccessHash: document.AccessHash,
			FileRef:    document.FileReference,
			MIME:       document.MimeType,
			Size:       document.Size,
		}}
	default:
		return nil
	}
}

func largestPhotoSize(photo *tg.Photo) string {
	for _, v := range slices.Backward(photo.Sizes) {
		if size := v.GetType(); size != "" {
			return size
		}
	}
	return "x"
}
