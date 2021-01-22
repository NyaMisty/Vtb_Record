package utils

import (
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//var client *http.Client

func init() {
	//client = createClient()
}

func MapToStruct(mapVal map[string]interface{}, structVal interface{}) error {
	config := &mapstructure.DecoderConfig{
		Metadata:         nil,
		Result:           structVal,
		WeaklyTypedInput: true,
	}
	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return err
	}
	decoder.Decode(mapVal)
	return nil
}

func IsFileExist(aFilepath string) bool {
	if _, err := os.Stat(aFilepath); err == nil {
		return true
	} else {
		//log.Errorf("File not exist %s, stat err %s", aFilepath, err)
		return false
	}
}
func GenerateFilepath(DownDir string, VideoTitle string) string {
	pathSlice := []string{DownDir, VideoTitle}
	aFilepath := strings.Join(pathSlice, "/")
	/*if IsFileExist(aFilepath) {
		return ChangeName(aFilepath)
	} else {
		return aFilepath
	}*/
	return ChangeName(aFilepath)
}
func MakeDir(dirPath string) (ret string, err error) {
	//if !IsFileExist(dirPath) {
	if true {
		//err := os.MkdirAll(dirPath, 0775)
		err = MkdirAll(dirPath)
		if err != nil {
			log.Errorf("mkdir error: %s, err: %s", dirPath, err)
			ret = ""
			return
		}
		return dirPath, nil
	}
	return dirPath, nil
}

func AddSuffix(aFilepath string, suffix string) string {
	dir, file := filepath.Split(aFilepath)
	ext := path.Ext(file)
	filename := strings.TrimSuffix(path.Base(file), ext)
	filename += "_"
	filename += suffix
	return dir + filename + ext
}

func ChangeName(aFilepath string) string {
	return AddSuffix(aFilepath, strconv.FormatInt(time.Now().Unix(), 10))
}
func GetTimeNow() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
func RemoveIllegalChar(Title string) string {
	illegalChars := []string{"|", "/", "\\", ":", "?"}
	//"github.com/fzxiao233/Go-Emoji-Utils"
	//Title = emoji.RemoveAll(Title)
	for _, char := range illegalChars {
		Title = strings.ReplaceAll(Title, char, "#")
	}
	return Title
}

func I2b(i int) bool {
	if i != 0 {
		return true
	} else {
		return false
	}
}

func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func Max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func RPartition(s string, sep string) (string, string, string) {
	parts := strings.SplitAfter(s, sep)
	if len(parts) == 1 {
		return "", "", parts[0]
	}
	return strings.Join(parts[0:len(parts)-1], ""), sep, parts[len(parts)-1]
}

func RandChooseStr(arr []string) string {
	return arr[rand.Intn(len(arr))]
}

func GenRandBuf(p []byte) (n int, err error) {
	r := rand.NewSource(time.Now().Unix())
	todo := len(p)
	offset := 0
	for {
		val := int64(r.Int63())
		for i := 0; i < 8; i++ {
			p[offset] = byte(val & 0xff)
			todo--
			if todo == 0 {
				return len(p), nil
			}
			offset++
			val >>= 8
		}
	}

	panic("unreachable")
}
