package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"

	pebbledb "github.com/cockroachdb/pebble"
	"github.com/go-faster/errors"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/contrib/pebble"
	"github.com/gotd/contrib/storage"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/time/rate"
	lj "gopkg.in/natefinch/lumberjack.v2"

	"github.com/gotd/td/examples"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/tg"
)

// stolen from https://github.com/gotd/td/blob/main/examples/userbot/main.go

func sessionFolder(phone string) string {
	var out []rune
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}
	return "phone-" + string(out)
}

func run(ctx context.Context) error {
	var arg struct {
		FillPeerStorage bool
	}
	flag.BoolVar(&arg.FillPeerStorage, "fill-peer-storage", false, "fill peer storage")
	flag.Parse()

	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return errors.Wrap(err, "load env")
	}

	phone := os.Getenv("TG_PHONE")
	if phone == "" {
		return errors.New("no phone")
	}
	appID, err := strconv.Atoi(os.Getenv("APP_ID"))
	if err != nil {
		return errors.Wrap(err, " parse app id")
	}
	appHash := os.Getenv("APP_HASH")
	if appHash == "" {
		return errors.New("no app hash")
	}

	replyMsg := os.Getenv("REPLY_MSG")
	if replyMsg == "" {
		return errors.New("no reply msg")
	}

	sessionDir := filepath.Join("session", sessionFolder(phone))
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return err
	}
	logFilePath := filepath.Join(sessionDir, "log.jsonl")

	fmt.Printf("Storing session in %s, logs in %s\n", sessionDir, logFilePath)

	logWriter := zapcore.AddSync(&lj.Logger{
		Filename:   logFilePath,
		MaxBackups: 3,
		MaxSize:    1,
		MaxAge:     7,
	})
	logCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		logWriter,
		zap.DebugLevel,
	)
	lg := zap.New(logCore)
	defer func() { _ = lg.Sync() }()

	sessionStorage := &telegram.FileSessionStorage{
		Path: filepath.Join(sessionDir, "session.json"),
	}

	db, err := pebbledb.Open(filepath.Join(sessionDir, "peers.pebble.db"), &pebbledb.Options{})
	if err != nil {
		return errors.Wrap(err, "create pebble storage")
	}
	peerDB := pebble.NewPeerStorage(db)
	lg.Info("Storage", zap.String("path", sessionDir))

	dispatcher := tg.NewUpdateDispatcher()

	waiter := floodwait.NewWaiter().WithCallback(func(ctx context.Context, wait floodwait.FloodWait) {
		lg.Warn("Flood wait", zap.Duration("wait", wait.Duration))
		fmt.Println("Got FLOOD_WAIT. Will retry after", wait.Duration)
	})

	options := telegram.Options{
		Logger:         lg,
		SessionStorage: sessionStorage,
		UpdateHandler:  dispatcher,
		Middlewares: []telegram.Middleware{
			waiter,
			ratelimit.New(rate.Every(time.Millisecond*100), 5),
		},
	}
	client := telegram.NewClient(appID, appHash, options)
	api := client.API()

	resolver := storage.NewResolverCache(peer.Plain(api), peerDB)
	_ = resolver

	startTime := time.Now()

	lastReminder := make(map[int64]time.Time)

	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil
		}

		if msg.Out {
			return nil
		}

		msgTime := time.Unix(int64(msg.Date), 0)
		if msgTime.Before(startTime) {
			return nil
		}

		if peer, ok := msg.PeerID.(*tg.PeerUser); ok {
			userID := peer.UserID
			fmt.Printf("Got message from user %d: %q\n", userID, msg.Message)

			if _, ok := lastReminder[userID]; ok {
				if sameDay(time.Now(), lastReminder[userID]) {
					return nil
				}
			}

			lastReminder[userID] = time.Now()

			_, err := client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer: &tg.InputPeerUser{
					UserID: userID,
				},
				Message:  replyMsg,
				RandomID: time.Now().UnixNano(),
			})
			if err != nil {
				log.Printf("send error: %v", err)
			}
		}

		return nil
	})

	flow := auth.NewFlow(examples.Terminal{PhoneNumber: phone}, auth.SendCodeOptions{})

	return waiter.Run(ctx, func(ctx context.Context) error {
		return client.Run(ctx, func(ctx context.Context) error {
			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return errors.Wrap(err, "auth")
			}

			self, err := client.Self(ctx)
			if err != nil {
				return errors.Wrap(err, "call self")
			}

			name := self.FirstName
			if self.Username != "" {
				name = fmt.Sprintf("%s (@%s)", name, self.Username)
			}
			fmt.Println("Current user:", name)

			lg.Info("Login",
				zap.String("first_name", self.FirstName),
				zap.String("last_name", self.LastName),
				zap.String("username", self.Username),
				zap.Int64("id", self.ID),
			)

			if arg.FillPeerStorage {
				fmt.Println("Filling peer storage from dialogs to cache entities")
				collector := storage.CollectPeers(peerDB)
				if err := collector.Dialogs(ctx, query.GetDialogs(api).Iter()); err != nil {
					return errors.Wrap(err, "collect peers")
				}
				fmt.Println("Filled")
			}

			fmt.Println("Listening for updates. Interrupt (Ctrl+C) to stop.")
			<-ctx.Done()
			return ctx.Err()
		})
	})
}

func sameDay(t1, t2 time.Time) bool {
	y1, m1, d1 := t1.Date()
	y2, m2, d2 := t2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			fmt.Println("\rClosed")
			os.Exit(0)
		}
		_, _ = fmt.Fprintf(os.Stderr, "Error: %+v\n", err)
		os.Exit(1)
	} else {
		fmt.Println("Done")
		os.Exit(0)
	}
}
