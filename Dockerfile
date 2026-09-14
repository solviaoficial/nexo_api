FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /nexo .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 nexo && adduser -D -u 10001 -G nexo nexo
COPY --from=build /nexo /usr/local/bin/nexo
WORKDIR /app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nexo"]
