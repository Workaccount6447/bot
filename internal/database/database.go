package database

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrUserAlreadyBanned = errors.New("user already banned")
	ErrUserNotBanned     = errors.New("user not banned")
	ErrLinkNotFound      = errors.New("link not found")
)

type User struct {
	ID    int64 `bson:"id"`
	Links int64 `bson:"links"`
	// Own storage channel. 0 = use the owner's channel (files expire).
	CustomChannelID    int64  `bson:"custom_channel_id,omitempty"`
	CustomChannelHash  int64  `bson:"custom_channel_hash,omitempty"`
	CustomChannelTitle string `bson:"custom_channel_title,omitempty"`
}

type BannedUser struct {
	ID      int64     `bson:"id"`
	BanDate time.Time `bson:"ban_date"`
}

// FileLink is shared with the stream repo (it reads these fields).
// Old records only have user_id/message_id/created_at and no link_id ("legacy").
type FileLink struct {
	LinkID            string     `bson:"link_id,omitempty"`
	BatchID           string     `bson:"batch_id,omitempty"`
	UserID            int64      `bson:"user_id"`
	ChannelID         int64      `bson:"channel_id,omitempty"`
	ChannelAccessHash int64      `bson:"channel_access_hash,omitempty"`
	Custom            bool       `bson:"custom"`
	MessageID         int        `bson:"message_id"`
	LogMsgID          int        `bson:"log_msg_id,omitempty"` // caption message in the owner channel
	FileName          string     `bson:"file_name,omitempty"`
	FileSize          int64      `bson:"file_size,omitempty"`
	MimeType          string     `bson:"mime_type,omitempty"`
	CreatedAt         time.Time  `bson:"created_at"`
	ExpiresAt         *time.Time `bson:"expires_at,omitempty"` // nil = never
	NotifyChatID      int64      `bson:"notify_chat_id,omitempty"`
	NotifyMsgID       int        `bson:"notify_msg_id,omitempty"` // the bot message that shows the links
	NotifyHash        int64      `bson:"notify_hash,omitempty"`   // user access hash, so edits work after a restart
	NotifyDone        bool       `bson:"notify_done,omitempty"`
	Attempts          int        `bson:"attempts,omitempty"`
}

func (l *FileLink) IsLegacy() bool { return l.LinkID == "" }
func (l *FileLink) Expired(now time.Time) bool {
	return l.ExpiresAt != nil && !l.ExpiresAt.After(now)
}

type DB struct {
	client *mongo.Client
	users  *mongo.Collection
	banned *mongo.Collection
	links  *mongo.Collection
}

var instance *DB

func Init(uri string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return err
	}
	db := client.Database("fsb")
	instance = &DB{
		client: client,
		users:  db.Collection("users"),
		banned: db.Collection("blacklist"),
		links:  db.Collection("links"),
	}
	bg := context.Background()
	instance.links.Indexes().CreateMany(bg, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "link_id", Value: 1}}, Options: options.Index().SetUnique(true).
			SetPartialFilterExpression(bson.M{"link_id": bson.M{"$exists": true}})},
		{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetSparse(true)},
		{Keys: bson.D{{Key: "batch_id", Value: 1}}, Options: options.Index().SetSparse(true)},
	})
	return nil
}

func IsEnabled() bool { return instance != nil }
func GetDB() *DB      { return instance }

// ---- users -------------------------------------------------------------------

func (d *DB) AddUser(ctx context.Context, id int64) error {
	_, err := d.users.InsertOne(ctx, User{ID: id})
	return err
}

func (d *DB) GetUser(ctx context.Context, id int64) (*User, error) {
	var u User
	err := d.users.FindOne(ctx, bson.M{"id": id}).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrUserNotFound
	}
	return &u, err
}

func (d *DB) UserExists(ctx context.Context, id int64) bool {
	_, err := d.GetUser(ctx, id)
	return err == nil
}

func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	_, err := d.users.DeleteMany(ctx, bson.M{"id": id})
	return err
}

func (d *DB) TotalUsers(ctx context.Context) (int64, error) {
	return d.users.CountDocuments(ctx, bson.M{})
}

func (d *DB) GetAllUsers(ctx context.Context) (*mongo.Cursor, error) {
	return d.users.Find(ctx, bson.M{})
}

func (d *DB) IncrLinks(ctx context.Context, id int64, n int64) {
	d.users.UpdateOne(ctx, bson.M{"id": id}, bson.M{"$inc": bson.M{"links": n}})
}

// SetCustomChannel makes the user's own channel the storage target (files never expire).
func (d *DB) SetCustomChannel(ctx context.Context, userID, channelID, hash int64, title string) error {
	_, err := d.users.UpdateOne(ctx, bson.M{"id": userID}, bson.M{"$set": bson.M{
		"custom_channel_id": channelID, "custom_channel_hash": hash, "custom_channel_title": title,
	}}, options.Update().SetUpsert(true))
	return err
}

func (d *DB) ClearCustomChannel(ctx context.Context, userID int64) error {
	_, err := d.users.UpdateOne(ctx, bson.M{"id": userID}, bson.M{"$unset": bson.M{
		"custom_channel_id": "", "custom_channel_hash": "", "custom_channel_title": "",
	}})
	return err
}

// ---- bans --------------------------------------------------------------------

func (d *DB) BanUser(ctx context.Context, id int64) error {
	if d.IsUserBanned(ctx, id) {
		return ErrUserAlreadyBanned
	}
	_, err := d.banned.InsertOne(ctx, BannedUser{ID: id, BanDate: time.Now()})
	return err
}

func (d *DB) UnbanUser(ctx context.Context, id int64) error {
	if !d.IsUserBanned(ctx, id) {
		return ErrUserNotBanned
	}
	_, err := d.banned.DeleteOne(ctx, bson.M{"id": id})
	return err
}

func (d *DB) IsUserBanned(ctx context.Context, id int64) bool {
	n, _ := d.banned.CountDocuments(ctx, bson.M{"id": id})
	return n > 0
}

func (d *DB) TotalBanned(ctx context.Context) (int64, error) {
	return d.banned.CountDocuments(ctx, bson.M{})
}

// ---- links -------------------------------------------------------------------

func (d *DB) TotalLinks(ctx context.Context) (int64, error) {
	return d.links.CountDocuments(ctx, bson.M{})
}

func (d *DB) InsertLink(ctx context.Context, l *FileLink) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	_, err := d.links.InsertOne(ctx, l)
	return err
}

func (d *DB) GetLink(ctx context.Context, linkID string) (*FileLink, error) {
	var l FileLink
	err := d.links.FindOne(ctx, bson.M{"link_id": linkID}).Decode(&l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrLinkNotFound
	}
	return &l, err
}

func (d *DB) DeleteLink(ctx context.Context, linkID string) error {
	_, err := d.links.DeleteOne(ctx, bson.M{"link_id": linkID})
	return err
}

// DeleteLegacyByMessage removes an old-style record for (user, message).
func (d *DB) DeleteLegacyByMessage(ctx context.Context, userID int64, messageID int) int64 {
	res, err := d.links.DeleteMany(ctx, bson.M{"user_id": userID, "message_id": messageID, "link_id": bson.M{"$exists": false}})
	if err != nil {
		return 0
	}
	return res.DeletedCount
}

// SetBatchExpiry restarts the expiry clock for every item of a batch and records
// which bot message shows them. Called by /done.
func (d *DB) FinalizeBatch(ctx context.Context, linkIDs []string, expiresAt *time.Time, notifyChat int64, notifyMsg int, notifyHash int64) error {
	set := bson.M{"notify_chat_id": notifyChat, "notify_msg_id": notifyMsg, "notify_hash": notifyHash}
	update := bson.M{"$set": set}
	if expiresAt != nil {
		set["expires_at"] = *expiresAt
	}
	_, err := d.links.UpdateMany(ctx, bson.M{"link_id": bson.M{"$in": linkIDs}}, update)
	return err
}

func (d *DB) SetNotify(ctx context.Context, linkID string, chatID int64, msgID int, hash int64) error {
	_, err := d.links.UpdateOne(ctx, bson.M{"link_id": linkID},
		bson.M{"$set": bson.M{"notify_chat_id": chatID, "notify_msg_id": msgID, "notify_hash": hash}})
	return err
}

// DueLinks returns links whose expiry time has passed.
func (d *DB) DueLinks(ctx context.Context, now time.Time, limit int64) ([]FileLink, error) {
	cur, err := d.links.Find(ctx, bson.M{"expires_at": bson.M{"$lte": now}},
		options.Find().SetSort(bson.D{{Key: "expires_at", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []FileLink
	return out, cur.All(ctx, &out)
}

// LinksByNotify returns every link shown in one bot message.
func (d *DB) LinksByNotify(ctx context.Context, chatID int64, msgID int) ([]FileLink, error) {
	cur, err := d.links.Find(ctx, bson.M{"notify_chat_id": chatID, "notify_msg_id": msgID},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []FileLink
	return out, cur.All(ctx, &out)
}

func (d *DB) MarkNotified(ctx context.Context, chatID int64, msgID int) {
	d.links.UpdateMany(ctx, bson.M{"notify_chat_id": chatID, "notify_msg_id": msgID},
		bson.M{"$set": bson.M{"notify_done": true}})
}

func (d *DB) BumpAttempts(ctx context.Context, linkIDs []string) {
	d.links.UpdateMany(ctx, bson.M{"link_id": bson.M{"$in": linkIDs}}, bson.M{"$inc": bson.M{"attempts": 1}})
}

func (d *DB) DeleteLinks(ctx context.Context, linkIDs []string) error {
	_, err := d.links.DeleteMany(ctx, bson.M{"link_id": bson.M{"$in": linkIDs}})
	return err
}

// GetUserFiles returns one page of a user's links, newest first (legacy + new).
func (d *DB) GetUserFiles(ctx context.Context, userID int64, page, perPage int) ([]FileLink, int64, error) {
	filter := bson.M{"user_id": userID}
	total, err := d.links.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	cur, err := d.links.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64(page*perPage)).SetLimit(int64(perPage)))
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	var files []FileLink
	if err := cur.All(ctx, &files); err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// PopUserFiles deletes ALL of a user's links and returns what was deleted so the
// caller can also remove the stored messages. Used by /clearfiles.
func (d *DB) PopUserFiles(ctx context.Context, userID int64) ([]FileLink, error) {
	cur, err := d.links.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	var all []FileLink
	if err := cur.All(ctx, &all); err != nil {
		return nil, err
	}
	if _, err := d.links.DeleteMany(ctx, bson.M{"user_id": userID}); err != nil {
		return nil, err
	}
	return all, nil
}

type UserStats struct {
	TotalFiles int64
	LinksCount int64
	NewestFile *FileLink
}

func (d *DB) GetUserStats(ctx context.Context, userID int64) (*UserStats, error) {
	filter := bson.M{"user_id": userID}
	total, err := d.links.CountDocuments(ctx, filter)
	if err != nil {
		return nil, err
	}
	var newest FileLink
	var ptr *FileLink
	if err := d.links.FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}})).Decode(&newest); err == nil {
		ptr = &newest
	}
	var count int64
	if u, err := d.GetUser(ctx, userID); err == nil {
		count = u.Links
	}
	return &UserStats{TotalFiles: total, LinksCount: count, NewestFile: ptr}, nil
}
