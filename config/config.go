package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var ValueOf = &config{}

type allowedUsers []int64

func (au *allowedUsers) Decode(value string) error {
	if value == "" {
		return nil
	}
	for _, id := range strings.Split(value, ",") {
		n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil {
			return err
		}
		*au = append(*au, n)
	}
	return nil
}

type config struct {
	ApiID    int32  `envconfig:"API_ID" required:"true"`
	ApiHash  string `envconfig:"API_HASH" required:"true"`
	BotToken string `envconfig:"BOT_TOKEN" required:"true"`

	// Owner/shared storage channel. Files of users WITHOUT their own channel go here
	// and are deleted after FILE_EXPIRY_HOURS. The bot must be admin here.
	LogChannelID int64 `envconfig:"LOG_CHANNEL" required:"true"`

	// Shared MongoDB with the stream repo (required).
	DatabaseURL string `envconfig:"DATABASE_URL" required:"true"`
	// Same value as in the stream repo (falls back to BOT_TOKEN).
	HashSecret string `envconfig:"HASH_SECRET"`
	// Public URL(s) of fsb-stream instances (traffic divider). Any of:
	//   STREAM_HOST=https://s1.example.com
	//   STREAM_HOST2=https://s2.example.com   (STREAM_HOST3, STREAM_HOST4 ... any number)
	//   STREAM_HOSTS=https://s1.example.com,https://s2.example.com
	// Every link is assigned to one of them, spread evenly. All instances must
	// share the same DATABASE_URL / HASH_SECRET / BOT_TOKEN / LOG_CHANNEL.
	StreamHost  string `envconfig:"STREAM_HOST"`
	StreamHosts string `envconfig:"STREAM_HOSTS"`
	// resolved list, built in Load()
	Hosts      []string
	HashLength int `envconfig:"HASH_LENGTH" default:"16"`

	// ---- expiry for files stored in the OWNER channel
	// Hours until a file in the owner channel is deleted and its links die. 0 = never.
	FileExpiryHours float64 `envconfig:"FILE_EXPIRY_HOURS" default:"6"`
	// How often the cleaner runs (seconds).
	ExpiryCheckSec int `envconfig:"EXPIRY_CHECK_SEC" default:"60"`

	// ---- multi-link (batch) mode
	MaxBatch         int `envconfig:"MAX_BATCH" default:"25"`
	BatchTimeoutMins int `envconfig:"BATCH_TIMEOUT_MINS" default:"15"`

	Dev            bool         `envconfig:"DEV" default:"false"`
	Port           int          `envconfig:"PORT" default:"8080"` // tiny health endpoint for PaaS hosts
	AllowedUsers   allowedUsers `envconfig:"ALLOWED_USERS"`
	OwnerID        int64        `envconfig:"OWNER_ID"`
	ULogChannelID  int64        `envconfig:"ULOG_CHANNEL"`
	UpdatesChannel string       `envconfig:"UPDATES_CHANNEL" default:""`
	StartPic       string       `envconfig:"START_PIC" default:""`
	BotName        string       `envconfig:"BOT_NAME" default:"File to Link"`
}

// FileExpiry returns the owner-channel retention, 0 if files never expire.
func (c *config) FileExpiry() time.Duration {
	if c.FileExpiryHours <= 0 {
		return 0
	}
	return time.Duration(c.FileExpiryHours * float64(time.Hour))
}

func SetFlagsFromConfig(cmd *cobra.Command) {
	cmd.Flags().Bool("dev", false, "Enable development mode")
	cmd.Flags().IntP("port", "p", 0, "Health-check port")
}

func Load(log *zap.Logger, cmd *cobra.Command) {
	log = log.Named("Config")
	if err := godotenv.Load(filepath.Clean("fsb.env")); err != nil && !os.IsNotExist(err) {
		log.Fatal("Error parsing fsb.env", zap.Error(err))
	}
	if v, _ := cmd.Flags().GetBool("dev"); v {
		os.Setenv("DEV", "true")
	}
	if v, _ := cmd.Flags().GetInt("port"); v != 0 {
		os.Setenv("PORT", strconv.Itoa(v))
	}
	if err := envconfig.Process("", ValueOf); err != nil {
		log.Fatal("Error while parsing env variables", zap.Error(err))
	}
	c := ValueOf
	c.LogChannelID = StripChannelID(c.LogChannelID)
	c.Hosts = collectHosts(c)
	if len(c.Hosts) == 0 {
		log.Fatal("Set STREAM_HOST (and optionally STREAM_HOST2, STREAM_HOST3… or STREAM_HOSTS) to your fsb-stream URL(s)")
	}
	c.StreamHost = c.Hosts[0]
	log.Sugar().Infof("Traffic divider: %d stream instance(s): %v", len(c.Hosts), c.Hosts)
	if c.HashSecret == "" {
		c.HashSecret = c.BotToken
		log.Warn("HASH_SECRET not set, using BOT_TOKEN. Set the same HASH_SECRET in the stream repo.")
	}
	if c.HashLength < 5 || c.HashLength > 64 {
		c.HashLength = 16
	}
	if c.ExpiryCheckSec < 10 {
		c.ExpiryCheckSec = 60
	}
	if c.MaxBatch < 2 {
		c.MaxBatch = 25
	}
	if c.BatchTimeoutMins < 1 {
		c.BatchTimeoutMins = 15
	}
	log.Sugar().Infof("Owner-channel files expire after %v (0 = never)", c.FileExpiry())
}

// StripChannelID turns -1001234567890 (or 1234567890) into 1234567890.
func StripChannelID(id int64) int64 {
	if id < 0 {
		id = -id
	}
	s := strconv.FormatInt(id, 10)
	if strings.HasPrefix(s, "100") && len(s) > 10 {
		s = s[3:]
	}
	out, _ := strconv.ParseInt(s, 10, 64)
	return out
}

var numberedHost = regexp.MustCompile(`^STREAM_HOST_?(\d+)=(.*)$`)

// collectHosts merges STREAM_HOST, STREAM_HOST2.., STREAM_HOSTS (deduplicated, ordered).
func collectHosts(c *config) []string {
	var hosts []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.TrimRight(strings.TrimSpace(h), "/")
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	add(c.StreamHost)
	type nh struct {
		n int
		h string
	}
	var nums []nh
	for _, e := range os.Environ() {
		if m := numberedHost.FindStringSubmatch(e); m != nil {
			n, _ := strconv.Atoi(m[1])
			nums = append(nums, nh{n, m[2]})
		}
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i].n < nums[j].n })
	for _, x := range nums {
		add(x.h)
	}
	for _, h := range strings.Split(c.StreamHosts, ",") {
		add(h)
	}
	return hosts
}
