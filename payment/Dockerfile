FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app
COPY payment-server-linux .

EXPOSE 8000

CMD ["./payment-server-linux"]
