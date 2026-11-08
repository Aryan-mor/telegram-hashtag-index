FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hashtag-index ./cmd/hashtag-index

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app && mkdir /data && chown app /data
USER app
ENV DATA_DIR=/data
VOLUME /data
ENTRYPOINT ["/hashtag-index"]
COPY --from=build /out/hashtag-index /hashtag-index
CMD ["serve"]
