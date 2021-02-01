module github.com/fzxiao233/Vtb_Record

go 1.13

require (
	github.com/bitly/go-simplejson v0.5.0
	github.com/etherlabsio/go-m3u8 v0.1.2
	github.com/fsnotify/fsnotify v1.4.9
	github.com/go-redis/redis/v8 v8.4.8
	github.com/gogf/greuse v1.1.0
	github.com/hashicorp/golang-lru v0.5.4
	github.com/knq/sdhook v0.0.0-20190801142816-0b7fa827d09a
	github.com/mitchellh/mapstructure v1.1.2
	github.com/orandin/lumberjackrus v1.0.1
	github.com/patrickmn/go-cache v2.1.0+incompatible
	github.com/rclone/rclone v1.52.2
	github.com/sirupsen/logrus v1.7.0
	github.com/spf13/viper v1.7.0
	github.com/stretchr/testify v1.6.1
	github.com/tidwall/gjson v1.6.0
	github.com/tidwall/pretty v1.0.1 // indirect
	github.com/umisama/go-regexpcache v0.0.0-20150417035358-2444a542492f
	github.com/valyala/bytebufferpool v1.0.0
	go.uber.org/atomic v1.7.0
	go.uber.org/ratelimit v0.1.0
	golang.org/x/net v0.0.0-20201202161906-c7110b5ffcbb
	golang.org/x/sync v0.0.0-20201020160332-67f06af15bc9
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	gopkg.in/natefinch/lumberjack.v2 v2.0.0 // indirect
	storj.io/common v0.0.0-20201204143755-a03c37168cb1
)

//replace github.com/rclone/rclone v1.52.2 => github.com/NyaMisty/rclone v1.52.2-mod
replace github.com/rclone/rclone v1.52.2 => ../../rclone/rclone

replace github.com/spf13/viper v1.7.0 => github.com/kublr/viper v1.6.3-0.20200316132607-0caa8e000d5b

//replace github.com/smallnest/ringbuffer => ../../smallnest/ringbuffer
