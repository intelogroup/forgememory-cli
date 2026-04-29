FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .

RUN go mod tidy && go build -o /payment-server .

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY --from=builder /payment-server .

EXPOSE 8000

CMD ["./payment-server"]
