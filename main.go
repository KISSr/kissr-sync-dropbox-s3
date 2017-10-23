package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/dropbox/dropbox-sdk-go-unofficial/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/dropbox/files"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"gopkg.in/redis.v3"
)

type ChangeSet struct {
	ListFolder struct {
		Accounts []string `json:"accounts"`
	} `json:"list_folder"`
}

type httpHandler struct{}

func (th *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Request: %s\n", r.URL.Path)
	if r.Method == "GET" {
		io.WriteString(w, r.URL.Query().Get("challenge"))
	} else {
		decoder := json.NewDecoder(r.Body)
		var changeSet ChangeSet
		err := decoder.Decode(&changeSet)
		checkErr(err)
		go applyChanges(changeSet)
	}
}

func Dropbox(token string) dropbox.Config {
	return dropbox.Config{
		Token: token,
	}
}

func shouldSync(path string, id string) bool {
	domain := path[1 : strings.Index(path[1:], "/")+1]
	db := postgresClient()
	defer db.Close()
	owned, err := db.Query("SELECT 1 FROM users JOIN sites ON users.id=sites.user_id WHERE dropbox_user_id=$1 and domain=$2", id, domain)
	checkErr(err)
	return owned.Next()
}

func copyToS3(path string, body io.Reader) {
	fmt.Println("Copying: ", path)
	uploader := s3manager.NewUploader(session.New(&aws.Config{}))
	_, _ = uploader.Upload(&s3manager.UploadInput{
		Body:        body,
		Bucket:      aws.String(os.Getenv("AWS_BUCKET")),
		Key:         aws.String(path),
		ACL:         aws.String("public-read"),
		ContentType: aws.String(contentType(path)),
	})
}

func contentType(filename string) string {
	return strings.TrimSuffix(
		mime.TypeByExtension(filepath.Ext(filename)),
		"; charset=utf-8",
	)
}

func syncToS3(userId string, token string) {
	r := redisClient()
	cursor, err := r.HGet("cursors", userId).Result()
	checkErr(err)
	config := dropbox.Config{
		Token: token,
	}
	dbx := files.New(config)

	var res *files.ListFolderResult
	var continue_arg *files.ListFolderContinueArg
	if cursor == "" {
		arg := files.NewListFolderArg("")
		arg.Recursive = true
		res, _ = dbx.ListFolder(arg)
		r.HSet("cursors", userId, res.Cursor)
	} else {
		continue_arg = files.NewListFolderContinueArg(cursor)
		res, _ = dbx.ListFolderContinue(continue_arg)
		r.HSet("cursors", userId, res.Cursor)
	}

	entries := res.Entries
	for res.HasMore {
		continue_arg = files.NewListFolderContinueArg(res.Cursor)
		res, _ = dbx.ListFolderContinue(continue_arg)
		r.HSet("cursors", userId, res.Cursor)
		entries = append(entries, res.Entries...)
	}

	for _, entry := range entries {
		switch f := entry.(type) {
		case *files.FileMetadata:
			download_arg := files.NewDownloadArg(f.PathDisplay)
			_, contents, _ := dbx.Download(download_arg)
			defer contents.Close()
			if shouldSync(f.PathDisplay, userId) {
				copyToS3(f.PathDisplay, contents)
			}
		}
	}
}

func redisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", os.Getenv("REDIS_PORT_6379_TCP_ADDR"), os.Getenv("REDIS_PORT_6379_TCP_PORT")),
		Password: "",
		DB:       0,
	})
}

func applyChanges(changeSet ChangeSet) {
	for _, userId := range changeSet.ListFolder.Accounts {
		syncToS3(userId, getDropboxToken(userId))
	}
}

func postgresClient() *sql.DB {
	var dbinfo string

	if os.Getenv("DB_USER") == "" {
		dbinfo = fmt.Sprintf("dbname=%s sslmode=disable",
			os.Getenv("DB_NAME"))
	} else {
		dbinfo = fmt.Sprintf("dbname=%s user=%s password=%s host=%s port=%s sslmode=require",
			os.Getenv("DB_NAME"),
			os.Getenv("DB_USER"),
			os.Getenv("DB_PASSWORD"),
			os.Getenv("DB_HOST"),
			os.Getenv("DB_PORT"),
		)
	}
	db, err := sql.Open("postgres", dbinfo)
	checkErr(err)
	return db
}
func getDropboxToken(id string) string {
	db := postgresClient()
	defer db.Close()
	rows, err := db.Query("SELECT token FROM users WHERE dropbox_user_id=$1", id)
	checkErr(err)

	for rows.Next() {
		var token string
		err = rows.Scan(&token)
		checkErr(err)
		return token
	}
	return ""
}

func main() {
	_ = godotenv.Load()
	http.ListenAndServe(":8080", &httpHandler{})
}

func checkErr(err error) {
	if err != nil {
		fmt.Printf(err.Error())
	}
}
