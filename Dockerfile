FROM golang:1.6-alpine
RUN apk add --no-cache git
ADD . /go/src/github.com/kissr/kissr-sync-dropbox-s3
RUN rm /go/src/github.com/kissr/kissr-sync-dropbox-s3/.env
RUN go get github.com/joho/godotenv
RUN go get github.com/aws/aws-sdk-go/aws
RUN go get gopkg.in/redis.v3
RUN go get github.com/lib/pq
RUN go get github.com/dropbox/dropbox-sdk-go-unofficial

RUN go get github.com/joho/godotenv
RUN go install github.com/kissr/kissr-sync-dropbox-s3
ENTRYPOINT /go/bin/kissr-sync-dropbox-s3
EXPOSE 8080
