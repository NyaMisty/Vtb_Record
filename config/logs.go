package config

import (
	"fmt"
	"github.com/fzxiao233/Vtb_Record/utils"
	"github.com/knq/sdhook"
	"github.com/orandin/lumberjackrus"
	"github.com/rclone/rclone/fs"
	"github.com/sirupsen/logrus"
	"io"
	"io/ioutil"
	"os"
	"path"
	"runtime"
	"strconv"
)

type LogWrapHook struct {
	Enabled  bool
	Hook     logrus.Hook
	LogLevel logrus.Level
}

func (h *LogWrapHook) Levels() []logrus.Level {
	return logrus.AllLevels[:logrus.DebugLevel+1]
}

func (h *LogWrapHook) Fire(entry *logrus.Entry) error {
	if !h.Enabled || entry.Level > h.LogLevel {
		return nil
	}
	return h.Hook.Fire(entry)
}

// WriterHook is a hook that writes logs of specified LogLevels to specified Writer
type WriterHook struct {
	Out       io.Writer
	Formatter logrus.Formatter
}

// Fire will be called when some logging function is called with current hook
// It will format logrus entry to string and write it to appropriate writer
func (hook *WriterHook) Fire(entry *logrus.Entry) error {
	serialized, err := hook.Formatter.Format(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to obtain reader, %v\n", err)
		return err
	}
	if _, err = hook.Out.Write(serialized); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write to logrus, %v\n", err)
	}
	return nil
}

// Levels define on which logrus levels this hook would trigger
func (hook *WriterHook) Levels() []logrus.Level {
	//return logrus.AllLevels[:hook.LogLevel+1]
	return logrus.AllLevels[:logrus.DebugLevel+1]
}

var ConsoleHook *LogWrapHook
var FileHook *LogWrapHook
var GoogleHook *LogWrapHook

// Can't be func init as we need the parsed config
func InitLog() {
	var err error

	logrus.Printf("Init logging!")
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetReportCaller(true)
	// Log as JSON instead of the default ASCII formatter.
	formatter := &logrus.TextFormatter{
		ForceColors: true,
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			filename := path.Base(f.File)
			_, _, shortfname := utils.RPartition(f.Function, ".")
			return fmt.Sprintf("%s()", shortfname), fmt.Sprintf("%s:%d", filename, f.Line)
		},
	}
	logrus.SetFormatter(formatter)

	ConsoleHook = &LogWrapHook{
		Enabled:  true,
		LogLevel: logrus.InfoLevel,
		Hook: &WriterHook{ // Send logs with level higher than warning to stderr
			Out:       logrus.StandardLogger().Out,
			Formatter: formatter,
		},
	}
	logrus.AddHook(ConsoleHook)
	logrus.StandardLogger().Out = ioutil.Discard

	fileHook, err := lumberjackrus.NewHook(
		&lumberjackrus.LogFile{
			Filename:   Config.LogFile,
			MaxSize:    Config.LogFileSize,
			MaxBackups: 1,
			MaxAge:     1,
			Compress:   false,
			LocalTime:  false,
		},
		logrus.DebugLevel,
		&logrus.JSONFormatter{},
		nil,
	)
	if err != nil {
		panic(fmt.Errorf("NewHook Error: %s", err))
	}

	FileHook = &LogWrapHook{
		Enabled:  true,
		Hook:     fileHook,
		LogLevel: logrus.DebugLevel,
	}

	logrus.AddHook(FileHook)

	googleHook, err := sdhook.New(
		sdhook.GoogleLoggingAgent(),
		sdhook.LogName(Config.LogFile+strconv.Itoa(os.Getpid())),
		sdhook.Levels(logrus.AllLevels[:logrus.DebugLevel+1]...),
	)
	if err != nil {
		logrus.WithField("prof", true).Warnf("Failed to initialize the sdhook: %v", err)
	} else {
		GoogleHook = &LogWrapHook{
			Enabled:  true,
			Hook:     googleHook,
			LogLevel: logrus.DebugLevel,
		}
		logrus.AddHook(GoogleHook)
	}

	fs.LogPrint = func(level fs.LogLevel, text string) {
		logrus.WithField("src", "rclone").Infof(fmt.Sprintf("%-6s: %s", level, text))
	}

	UpdateLogLevel()
}
