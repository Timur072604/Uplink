FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o server ./backend/cmd/server

FROM alpine:3.23
WORKDIR /app
RUN addgroup -S app && adduser -S user -G app
COPY --from=builder /app/server .

COPY backend/migrations ./migrations
COPY --from=builder /app/frontend/static ./frontend/static

USER user
EXPOSE 8080
CMD ["./server"]