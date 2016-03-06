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
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/stacktic/dropbox"
	"gopkg.in/redis.v3"
)

type ChangeSet struct {
	Delta struct {
		Users []int `json:"users"`
	} `json:"delta"`
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

func Dropbox(token string) *dropbox.Dropbox {
	db := dropbox.NewDropbox()
	db.SetAppInfo(
		os.Getenv("DROPBOX_KEY"),
		os.Getenv("DROPBOX_SECRET"))

	db.SetAccessToken(token)
	return db
}

func processDeltas(id int, page *dropbox.DeltaPage, db *dropbox.Dropbox) {
	for _, entry := range page.Entries {
		if entry.Entry == nil {
			deleteFromS3(entry.Path, db)
		} else if !entry.Entry.IsDir &&
			shouldSync(entry.Entry.Path, id) {
			copyToS3(entry.Entry.Path, db)
		}

		if page.HasMore {
			nextPage, err := db.Delta(page.Cursor.Cursor, "")
			checkErr(err)
			processDeltas(id, nextPage, db)
		}
	}
	r := redisClient()
	r.HSet("cursors", strconv.Itoa(id), page.Cursor.Cursor)
}

func shouldSync(path string, id int) bool {
	domain := path[1 : strings.Index(path[1:], "/")+1]
	db := postgresClient()
	defer db.Close()
	owned, err := db.Query("SELECT 1 FROM users JOIN sites ON users.id=sites.user_id WHERE dropbox_user_id=$1 and domain=$2", id, domain)
	checkErr(err)
	return owned.Next()
}

func deleteFromS3(path string, db *dropbox.Dropbox) {
	fmt.Println("Deleting: ", path)
	svc := s3.New(session.New())
	params := &s3.DeleteObjectInput{
		Bucket: aws.String(os.Getenv("AWS_BUCKET")),
		Key:    aws.String(path),
	}
	_, err := svc.DeleteObject(params)
	checkErr(err)
}

func copyToS3(path string, db *dropbox.Dropbox) {
	fmt.Println("Copying: ", path)
	file, _, err := db.Download(path, "", 0)
	uploader := s3manager.NewUploader(session.New(&aws.Config{}))
	_, err = uploader.Upload(&s3manager.UploadInput{
		Body:        file,
		Bucket:      aws.String(os.Getenv("AWS_BUCKET")),
		Key:         aws.String(path),
		ACL:         aws.String("public-read"),
		ContentType: aws.String(contentType(path)),
	})
	checkErr(err)
}

func contentType(filename string) string {
	return strings.TrimSuffix(
		mime.TypeByExtension(filepath.Ext(filename)),
		"; charset=utf-8",
	)
}

func syncToS3(id int, token string) {
	db := Dropbox(token)
	processDeltas(id, deltas(db, token, id), db)
}

func deltas(db *dropbox.Dropbox, token string, id int) *dropbox.DeltaPage {
	r := redisClient()

	cursor, err := r.HGet("cursors", strconv.Itoa(id)).Result()
	if err != redis.Nil {
		checkErr(err)
	}
	page, err := db.Delta(cursor, "")
	checkErr(err)

	return page
}

func redisClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", os.Getenv("REDIS_PORT_6379_TCP_ADDR"), os.Getenv("REDIS_PORT_6379_TCP_PORT")),
		Password: "",
		DB:       0,
	})
}

func applyChanges(changeSet ChangeSet) {
	for _, userId := range changeSet.Delta.Users {
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
func getDropboxToken(id int) string {
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
		panic(err)
	}
}
