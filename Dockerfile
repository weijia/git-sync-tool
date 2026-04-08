FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o git-sync-tool main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/git-sync-tool .

EXPOSE 8080

CMD ["./git-sync-tool"]