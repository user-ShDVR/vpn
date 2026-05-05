FROM node:20-alpine AS css
WORKDIR /app
COPY package.json tailwind.config.js ./
COPY web ./web
RUN npm install --silent && npx tailwindcss -i ./web/styles/input.css -o ./web/static/dist.css --minify

FROM golang:1.24-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1001
COPY . .
COPY --from=css /app/web/static/dist.css ./web/static/dist.css
RUN templ generate ./web/...
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vpn-api ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /vpn-api .
COPY --from=builder /app/web/static ./web/static
COPY migrations ./migrations
EXPOSE 8080
CMD ["/app/vpn-api"]
