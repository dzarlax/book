FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /book ./cmd/book

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /book .
COPY migrations/ migrations/
COPY internal/ui/templates/ internal/ui/templates/
COPY internal/ui/static/ internal/ui/static/
EXPOSE 8080
CMD ["./book"]
